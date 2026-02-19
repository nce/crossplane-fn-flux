package main

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"

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
						"cluster": {
							Items: []*fnv1.Resource{
								{
									Resource: resource.MustStructJSON(`{
										"apiVersion": "eks.aws.m.upbound.io/v1beta1",
										"kind": "Cluster",
										"status": {
											"atProvider": {
												"arn": "test-arn"
											}
										}
									}`),
								},
							},
						},
					},
					Observed: &fnv1.State{
						Composite: &fnv1.Resource{
							Resource: resource.MustStructJSON(`{
								"apiVersion": "internal.ab.de",
								"kind": "OnlineFluxRemoteConnection",
								"metadata": {
									"name": "test",
									"namespace": "foobar"
								},
								"spec": {
									"clusterName": "test1"
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
							"cluster": {
								ApiVersion: "eks.aws.m.upbound.io/v1beta1",
								Kind:       "Cluster",
								Match: &fnv1.ResourceSelector_MatchName{
									MatchName: "test1",
								},
								Namespace: proto.String("foobar"),
							},
						},
					},
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
							"test": {Resource: resource.MustStructJSON(`{
								"apiVersion": "s3.aws.m.upbound.io/v1beta1",
								"kind": "Bucket",
								"metadata": {
									"name": "test-arn",
									"annotations": {
										"crossplane.io/external-name": "foo"
									}
								},
								"spec": {
									"forProvider": {
										"region": "test-arn"
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

			if diff := cmp.Diff(tc.want.rsp, rsp, protocmp.Transform()); diff != "" {
				t.Errorf("%s\nf.RunFunction(...): -want rsp, +got rsp:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nf.RunFunction(...): -want err, +got err:\n%s", tc.reason, diff)
			}
		})
	}
}
