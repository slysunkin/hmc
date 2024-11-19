// Copyright 2024
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sveltos

import (
	"testing"

	sveltosv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetStatusConditions(t *testing.T) {
	releaseNamespace := "testnamespace"
	releaseName := "testname"
	conflictMsg := "some conflict message"
	failureMesg := "some failure message"

	for _, tc := range []struct {
		err             error
		expectCondition metav1.Condition
		name            string
		summary         sveltosv1beta1.ClusterSummary
	}{
		{
			name: "sveltos featuresummary provisioning",
			summary: sveltosv1beta1.ClusterSummary{
				Status: sveltosv1beta1.ClusterSummaryStatus{
					FeatureSummaries: []sveltosv1beta1.FeatureSummary{
						{
							FeatureID: sveltosv1beta1.FeatureHelm,
							Status:    sveltosv1beta1.FeatureStatusProvisioning,
						},
					},
				},
			},
			expectCondition: metav1.Condition{
				Type:   string(sveltosv1beta1.FeatureHelm),
				Status: metav1.ConditionTrue,
				Reason: string(sveltosv1beta1.FeatureStatusProvisioning),
			},
		},
		{
			name: "sveltos featuresummary provisioned",
			summary: sveltosv1beta1.ClusterSummary{
				Status: sveltosv1beta1.ClusterSummaryStatus{
					FeatureSummaries: []sveltosv1beta1.FeatureSummary{
						{
							FeatureID: sveltosv1beta1.FeatureHelm,
							Status:    sveltosv1beta1.FeatureStatusProvisioned,
						},
					},
				},
			},
			expectCondition: metav1.Condition{
				Type:   string(sveltosv1beta1.FeatureHelm),
				Status: metav1.ConditionTrue,
				Reason: string(sveltosv1beta1.FeatureStatusProvisioned),
			},
		},
		{
			name: "sveltos featuresummary failed",
			summary: sveltosv1beta1.ClusterSummary{
				Status: sveltosv1beta1.ClusterSummaryStatus{
					FeatureSummaries: []sveltosv1beta1.FeatureSummary{
						{
							FeatureID:      sveltosv1beta1.FeatureHelm,
							Status:         sveltosv1beta1.FeatureStatusFailed,
							FailureMessage: &failureMesg,
						},
					},
				},
			},
			expectCondition: metav1.Condition{
				Type:    string(sveltosv1beta1.FeatureHelm),
				Status:  metav1.ConditionFalse,
				Reason:  string(sveltosv1beta1.FeatureStatusFailed),
				Message: failureMesg,
			},
		},
		{
			name: "sveltos helmreleasesummary managing",
			summary: sveltosv1beta1.ClusterSummary{
				Status: sveltosv1beta1.ClusterSummaryStatus{
					HelmReleaseSummaries: []sveltosv1beta1.HelmChartSummary{
						{
							ReleaseNamespace: releaseNamespace,
							ReleaseName:      releaseName,
							Status:           sveltosv1beta1.HelmChartStatusManaging,
						},
					},
				},
			},
			expectCondition: metav1.Condition{
				Type:    HelmReleaseReadyConditionType(releaseNamespace, releaseName),
				Status:  metav1.ConditionTrue,
				Reason:  string(sveltosv1beta1.HelmChartStatusManaging),
				Message: helmReleaseConditionMessage(releaseNamespace, releaseName, ""),
			},
		},
		{
			name: "sveltos helmreleasesummary conflict",
			summary: sveltosv1beta1.ClusterSummary{
				Status: sveltosv1beta1.ClusterSummaryStatus{
					HelmReleaseSummaries: []sveltosv1beta1.HelmChartSummary{
						{
							ReleaseNamespace: releaseNamespace,
							ReleaseName:      releaseName,
							Status:           sveltosv1beta1.HelmChartStatusConflict,
							ConflictMessage:  conflictMsg,
						},
					},
				},
			},
			expectCondition: metav1.Condition{
				Type:    HelmReleaseReadyConditionType(releaseNamespace, releaseName),
				Status:  metav1.ConditionFalse,
				Reason:  string(sveltosv1beta1.HelmChartStatusConflict),
				Message: helmReleaseConditionMessage(releaseNamespace, releaseName, conflictMsg),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conditions := []metav1.Condition{}
			require.NoError(t, SetStatusConditions(&tc.summary, &conditions))
			assert.Len(t, conditions, 1)
			assert.Equal(t, tc.expectCondition.Type, conditions[0].Type)
			assert.Equal(t, tc.expectCondition.Status, conditions[0].Status)
			assert.Equal(t, tc.expectCondition.Reason, conditions[0].Reason)
			assert.Equal(t, tc.expectCondition.Message, conditions[0].Message)
		})
	}
}
