package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/apis/common"
	eks "github.com/upbound/provider-aws/v2/apis/namespaced/eks/v1beta1"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	fncontext "github.com/crossplane/function-sdk-go/context"
	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/resource/composed"
	"github.com/crossplane/function-sdk-go/response"
)

// Function returns whatever response you ask it to.
type Function struct {
	fnv1.UnimplementedFunctionRunnerServiceServer

	log logging.Logger
}

//nolint:tagliatelle //format is given by cluster
type MangementCluster struct {
	AccountID     string `json:"management-cluster-id"`
	DefaultRegion string `json:"default-region"`
}

func getEksClusterSelector(xr *resource.Composite) (*fnv1.ResourceSelector, error) {
	clusterName, err := xr.Resource.GetString("spec.clusterName")
	if err != nil {
		return nil, fmt.Errorf("cannot read spec.clusterName field of %s", xr.Resource.GetKind())
	}

	res := &fnv1.ResourceSelector{
		ApiVersion: "eks.aws.m.upbound.io/v1beta1",
		Kind:       "Cluster",
		Namespace:  proto.String(xr.Resource.GetNamespace()),
		Match: &fnv1.ResourceSelector_MatchName{
			MatchName: clusterName,
		},
	}

	return res, nil
}

// RunFunction runs the Function.
func (f *Function) RunFunction(_ context.Context, req *fnv1.RunFunctionRequest) (*fnv1.RunFunctionResponse, error) {
	f.log.Info("Running function", "tag", req.GetMeta().GetTag())

	rsp := response.To(req, response.DefaultTTL)

	xr, err := request.GetObservedCompositeResource(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot get observed composite resource from %T", req))
		return rsp, nil
	}

	eksResourceSelector, err := getEksClusterSelector(xr)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot create Eks Selector for %T", req))
		return rsp, nil
	}

	// construct a response requirement to signal crossplane, that we need additional
	// resources in the function. We need an eks cluster to extract its endpoint
	rsp.Requirements = &fnv1.Requirements{
		Resources: map[string]*fnv1.ResourceSelector{
			"eks": eksResourceSelector,
		},
	}

	if len(req.GetRequiredResources()) == 0 {
		// if we have not populated our required ressources in our request, we signal
		// crossplane to fetch the requirements and rerun this function again
		f.log.Debug("requirements", "msg", "required resources not available")

		// this can happen up to 5 times
		return rsp, nil
	}

	if len(req.GetRequiredResources()["eks"].GetItems()) == 0 {
		// the api server was not able to fetch our depended ressources
		// we have to fail and wait until the requirements are fulfilled
		response.Fatal(rsp, fmt.Errorf("dependend eks cluster not available: %s/%s",
			eksResourceSelector.GetNamespace(),
			eksResourceSelector.GetMatchName(),
		))

		return rsp, nil
	}

	env := &unstructured.Unstructured{}
	if v, ok := request.GetContextKey(req, fncontext.KeyEnvironment); ok {
		if err := resource.AsObject(v.GetStructValue(), env); err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot get Composition environment from %T context key %q", req, fncontext.KeyEnvironment))
			return rsp, nil
		}

		// Todo: only query for mc-defaults EnvConf

		f.log.Debug("Loaded Composition environment from Function context", "context-key", fncontext.KeyEnvironment)
	}

	var mc MangementCluster

	d, err := json.Marshal(env.Object)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot decode environment into struct"))
		return rsp, nil
	}

	if err := json.Unmarshal(d, &mc); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot decode environment into struct"))
		return rsp, nil
	}

	if mc.AccountID == "" {
		response.Fatal(rsp, errors.New("cannot decode management-cluster account id from environment config"))
		return rsp, nil
	}

	_ = eks.AddToScheme(composed.Scheme)
	eksCluster := &eks.Cluster{}
	// transform the unstructered data into an eks resource
	if err := resource.AsObject(req.GetRequiredResources()["eks"].GetItems()[0].GetResource(), eksCluster); err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "could not parse eks cluster: %s/%s",
			eksResourceSelector.GetNamespace(),
			eksResourceSelector.GetMatchName(),
		))

		return rsp, nil
	}

	// Get all desired composed resources from the request. The function will
	// update this map of resources, then save it. This get, update, set pattern
	// ensures the function keeps any resources added by other functions.
	desired, err := request.GetDesiredComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot get desired resources from %T", req))
		return rsp, nil
	}

	clusterName, err := xr.Resource.GetString("spec.clusterName")
	if err != nil {
		return nil, fmt.Errorf("cannot read spec.clusterName field of %s", xr.Resource.GetKind())
	}

	providerConfig := &common.ProviderConfigReference{
		Name: "aws",
		Kind: "ProviderConfig",
	}

	var ca string

	if eksCluster.Status.AtProvider.CertificateAuthority != nil {
		raw := eksCluster.Status.AtProvider.CertificateAuthority[0].Data

		decoded, err := base64.StdEncoding.DecodeString(*raw)
		if err != nil {
			return nil, fmt.Errorf("cannot decode ca of cluster %s", xr.Resource.GetName())
		}

		ca = string(decoded)
	}

	_ = corev1.AddToScheme(composed.Scheme)
	configmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: xr.Resource.GetName(),
			Annotations: map[string]string{
				"crossplane.io/external-name": "flux-remote-connection",
			},
		},
		Data: map[string]string{
			"provider": "aws",
			"cluster":  *eksCluster.Status.AtProvider.Arn,
			"address":  *eksCluster.Status.AtProvider.Endpoint,
			"ca.crt":   *proto.String(ca),
		},
		// todo: validate these atprovider fields exists
	}

	cd, err := composed.From(configmap)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", configmap, &composed.Unstructured{}))
		return rsp, nil
	}

	desired[resource.Name("configmap")] = &resource.DesiredComposed{Resource: cd}

	accessEntry := &eks.AccessEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name: xr.Resource.GetName(),
			Annotations: map[string]string{
				"crossplane.io/external-name": "flux-remote-connection",
			},
		},
		Spec: eks.AccessEntrySpec{
			ForProvider: eks.AccessEntryParameters{
				Region:       &mc.DefaultRegion,
				ClusterName:  &clusterName,
				PrincipalArn: proto.String(fmt.Sprintf("arn:aws:iam::%s:role/flux-remote-connection", mc.AccountID)),
			},
		},
	}

	accessEntry.SetProviderConfigReference(providerConfig)

	cd, err = composed.From(accessEntry)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", accessEntry, &composed.Unstructured{}))
		return rsp, nil
	}

	desired[resource.Name("accessentry")] = &resource.DesiredComposed{Resource: cd}

	accesspolicyassociation := &eks.AccessPolicyAssociation{
		ObjectMeta: metav1.ObjectMeta{
			Name: xr.Resource.GetName(),
			Annotations: map[string]string{
				"crossplane.io/external-name": "flux-remote-connection",
			},
		},
		Spec: eks.AccessPolicyAssociationSpec{
			ForProvider: eks.AccessPolicyAssociationParameters{
				Region:       &mc.DefaultRegion,
				ClusterName:  &clusterName,
				PrincipalArn: proto.String(fmt.Sprintf("arn:aws:iam::%s:role/flux-remote-connection", mc.AccountID)),
				PolicyArn:    proto.String("arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"),
				AccessScope: &eks.AccessScopeParameters{
					Type: proto.String("cluster"),
				},
			},
		},
	}
	accesspolicyassociation.SetProviderConfigReference(providerConfig)

	cd, err = composed.From(accesspolicyassociation)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", accesspolicyassociation, &composed.Unstructured{}))
		return rsp, nil
	}

	desired[resource.Name("accesspolicyassociation")] = &resource.DesiredComposed{Resource: cd}

	// we should not used typed kustomization api;
	// leads to controller runtime mismatch
	ks := &unstructured.Unstructured{}
	ks.SetAPIVersion("kustomize.toolkit.fluxcd.io/v1")
	ks.SetKind("Kustomization")
	ks.SetName("healthcheck")

	ksanno := map[string]string{
		"crossplane.io/external-name": "flux-remote-connection",
	}
	ks.SetAnnotations(ksanno)
	ks.Object["spec"] = map[string]any{
		"interval": "5m",
		"path":     "./healthcheck",
		"prune":    true,
		"sourceRef": map[string]any{
			"kind":      "GitRepository",
			"namespace": "flux-system",
			"name":      "healthcheck",
		},
		"healthChecks": []any{
			map[string]any{
				"apiVersion": "v1",
				"kind":       "namespace",
				"name":       "healthcheck",
			},
		},
		"kubeConfig": map[string]any{
			"configMapRef": map[string]any{
				"name": configmap.GetName(),
			},
		},
	}

	cd, err = composed.From(ks)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", ks, &composed.Unstructured{}))
		return rsp, nil
	}

	desired[resource.Name("kustomization")] = &resource.DesiredComposed{Resource: cd}

	// Finally, save the updated desired composed resources to the response.
	if err := response.SetDesiredComposedResources(rsp, desired); err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot set desired composed resources in %T", rsp))
		return rsp, nil
	}

	response.ConditionTrue(rsp, "FunctionSuccess", "Success").
		TargetComposite()

	return rsp, nil
}
