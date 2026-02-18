package main

import (
	"context"
	"fmt"

	eks "github.com/upbound/provider-aws/v2/apis/namespaced/eks/v1beta1"
	s3 "github.com/upbound/provider-aws/v2/apis/namespaced/s3/v1beta1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"google.golang.org/protobuf/proto"

	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
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

// RunFunction runs the Function.
func (f *Function) RunFunction(_ context.Context, req *fnv1.RunFunctionRequest) (*fnv1.RunFunctionResponse, error) {
	f.log.Info("Running function", "tag", req.GetMeta().GetTag())

	rsp := response.To(req, response.DefaultTTL)

	xr, err := request.GetObservedCompositeResource(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot get observed composite resource from %T", req))
		return rsp, nil
	}

	clusterName, err := xr.Resource.GetString("spec.clusterName")
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot read spec.clusterName field of %s", xr.Resource.GetKind()))
		return rsp, nil
	}

	namespace := xr.Resource.GetNamespace()

	// f.log.Info("req", "foo", req.GetRequiredResources())

	//namespace := xr.Resource.GetNamespace()
	extra := make(map[string]*fnv1.ResourceSelector, 1)

	extra["cluster"] = &fnv1.ResourceSelector{
		Namespace:  proto.String(namespace),
		ApiVersion: "eks.aws.m.upbound.io/v1beta1",
		Kind:       "Cluster",
		// Match: &fnv1.ResourceSelector_MatchLabels{
		// 	MatchLabels: &fnv1.MatchLabels{Labels: map[string]string{"label": "foo"}},
		// },
		Match: &fnv1.ResourceSelector_MatchName{
			MatchName: clusterName,
		},
	}

	rsp.Requirements = &fnv1.Requirements{Resources: extra}
	fmt.Printf("Incoming Requirements: %+v\n", req.GetRequiredResources())
	// Check if cluster already provided
	if len(req.GetRequiredResources()) == 0 {
		//if req.GetRequiredResources()["cluster"] == nil {

		f.log.Info("dep", "baz", "dependend resource not found")

		return rsp, nil
	}

	fmt.Printf("Outgoing Requirements: %+v\n", rsp.Requirements)

	if len(req.GetRequiredResources()["cluster"].GetItems()) == 0 {
		return rsp, fmt.Errorf("could not retrieve cluster")
	}

	eksCluster := &eks.Cluster{}
	resource.AsObject(req.GetRequiredResources()["cluster"].GetItems()[0].GetResource(), eksCluster)

	// clusterNam, err := xr.Resource.GetString("spec.clusterName")
	// if err != nil {
	// 	response.Fatal(rsp, errors.Wrapf(err, "cannot read spec.region field of %s", xr.Resource.GetKind()))
	// 	return rsp, nil
	// }

	f.log.Info("proc", "baz", "processing depenencies")

	// Get all desired composed resources from the request. The function will
	// update this map of resources, then save it. This get, update, set pattern
	// ensures the function keeps any resources added by other functions.
	desired, err := request.GetDesiredComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot get desired resources from %T", req))
		return rsp, nil
	}

	_ = s3.AddToScheme(composed.Scheme)

	b := &s3.Bucket{
		ObjectMeta: metav1.ObjectMeta{
			Name: *eksCluster.Status.AtProvider.Arn,
			// Set the external name annotation to the desired bucket name.
			// This controls what the bucket will be named in AWS.
			Annotations: map[string]string{
				"crossplane.io/external-name": "foo",
			},
		},
		Spec: s3.BucketSpec{
			ForProvider: s3.BucketParameters{
				// Set the bucket's region to the value read from the XR.
				//Region: ptr.To[string](clusterName),
				Region: eksCluster.Status.AtProvider.Arn,
			},
		},
	}

	cd, err := composed.From(b)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", b, &composed.Unstructured{}))
		return rsp, nil
	}

	desired[resource.Name("test")] = &resource.DesiredComposed{Resource: cd}

	// Finally, save the updated desired composed resources to the response.
	if err := response.SetDesiredComposedResources(rsp, desired); err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot set desired composed resources in %T", rsp))
		return rsp, nil
	}

	// You can set a custom status condition on the XR. This allows you
	// to communicate with the user.
	response.ConditionTrue(rsp, "FunctionSuccess", "Success").
		TargetComposite()

	f.log.Info("proc", "baz", "processing depenencies2")

	return rsp, nil
}
