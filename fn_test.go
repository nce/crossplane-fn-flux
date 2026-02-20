package main

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"
)

func TestRunFunction(t *testing.T) {
	type args struct {
		ctx context.Context
		req *fnv1.RunFunctionRequest
	}

	type want struct {
		rsp *fnv1.RunFunctionResponse
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ResponseIsReturned": {
			reason: "The Function should return a valid output",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Meta: &fnv1.RequestMeta{Tag: "hello"},
					//				this is handed to the function
					RequiredResources: map[string]*fnv1.Resources{
						"eks": {
							Items: []*fnv1.Resource{
								{
									Resource: resource.MustStructJSON(`{
										"apiVersion": "eks.aws.m.upbound.io/v1beta1",
										"kind": "Cluster",
										"metadata": {
											"namespace": "XRamespace",
											"name": "clusterName"
										},
										"status": {
											"atProvider": {
												"arn": "clusterArn",
												"endpoint": "https://example.org",
												"certificateAuthority": [
													{
													 "data": "ZXhhbXBsZQ=="
													}
												]
											}
										}
									}`),
								},
							},
						},
					},
					Context: &structpb.Struct{
						Fields: map[string]*structpb.Value{
							"apiextensions.crossplane.io/environment": structpb.NewStructValue(resource.MustStructJSON(`{
								"management-cluster-id": "12345",
								"default-region": "eu"
							}`)),
						},
					},
					Observed: &fnv1.State{
						Composite: &fnv1.Resource{
							Resource: resource.MustStructJSON(`{
								"apiVersion": "internal.ab.de",
								"kind": "OnlineFluxRemoteConnection",
								"metadata": {
									"name": "XRName",
									"namespace": "XRNamespace"
								},
								"spec": {
									"clusterName": "clusterName"
								}
							}`),
						},
					},
				},
			},

			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta: &fnv1.ResponseMeta{Tag: "hello", Ttl: durationpb.New(response.DefaultTTL)},
					Requirements: &fnv1.Requirements{
						Resources: map[string]*fnv1.ResourceSelector{
							"eks": {
								ApiVersion: "eks.aws.m.upbound.io/v1beta1",
								Kind:       "Cluster",
								Namespace:  proto.String("XRNamespace"),
								Match: &fnv1.ResourceSelector_MatchName{
									MatchName: "clusterName",
								},
							},
						},
					},
					Context: &structpb.Struct{},
					Conditions: []*fnv1.Condition{
						{
							Type:   "FunctionSuccess",
							Status: fnv1.Status_STATUS_CONDITION_TRUE,
							Reason: "Success",
							Target: fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},

					Desired: &fnv1.State{
						Resources: map[string]*fnv1.Resource{
							"accessentry": {Resource: resource.MustStructJSON(`{
								"apiVersion": "eks.aws.m.upbound.io/v1beta1",
								"kind": "AccessEntry",
								"metadata": {
									"name": "XRName",
									"annotations": {
										"crossplane.io/external-name": "flux-remote-connection"
									}
								},
								"spec": {
									"forProvider": {
										"clusterName": "clusterName",
										"principalArn": "arn:aws:iam::12345:role/flux-remote-connection",
										"region": "eu"
									},
									"providerConfigRef": {
										"kind": "ProviderConfig",
										"name": "aws"
									}
								},
								"status": {
									"observedGeneration": 0
								}
							}`)},
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &Function{log: logging.NewNopLogger()}

			rsp, err := f.RunFunction(tc.args.ctx, tc.args.req)

			rspCopy := proto.Clone(rsp).(*fnv1.RunFunctionResponse)
			delete(rspCopy.Context.Fields, "apiextensions.crossplane.io/environment")
			delete(rspCopy.Desired.Resources, "configmap")
			delete(rspCopy.Desired.Resources, "kustomization")
			delete(rspCopy.Desired.Resources, "accesspolicyassociation")

			if diff := cmp.Diff(tc.want.rsp, rspCopy, protocmp.Transform()); diff != "" {
				t.Errorf("%s\nf.RunFunction(...): -want rsp, +got rsp:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nf.RunFunction(...): -want err, +got err:\n%s", tc.reason, diff)
			}
		})
	}
}
