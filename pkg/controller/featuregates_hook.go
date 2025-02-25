package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	internalfeatures "github.com/openshift/cluster-olm-operator/internal/featuregates"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/klog/v2"
)

const (
	operatorControllerDeploymentName = "operator-controller-controller-manager"
	catalogdDeploymentName           = "catalogd-controller-manager"
)

// UpdateDeploymentFeatureGatesHook handles setting --feature-gates container argument
// on 'manager' containers for both OLM deployments - catalogd and operator-controller
// with appropriate enabled feature gates
func UpdateDeploymentFeatureGatesHook(
	featuresAccessor featuregates.FeatureGateAccess,
	featuresMapper internalfeatures.MapperInterface,
) deploymentcontroller.DeploymentHookFunc {
	return func(_ *operatorv1.OperatorSpec, deployment *appsv1.Deployment) error {
		logger := klog.FromContext(context.Background()).WithName("feature_gates_hook")
		logger.V(0).Info("updating environment", "deployment", deployment.Name)

		clusterGatesConfig, err := featuresAccessor.CurrentFeatureGates()
		if err != nil {
			return fmt.Errorf("error getting featuregates.config.openshift.io/cluster: %w", err)
		}

		var upstreamEnabledGates, upstreamDisabledGates []string
		switch deployment.Name {
		case operatorControllerDeploymentName:
			upstreamEnabledGates, upstreamDisabledGates = upstreamFeatureGates(
				clusterGatesConfig,
				featuresMapper.UpstreamDefaults("operator-controller"),
				featuresMapper.OperatorControllerDownstreamFeatureGates(),
				featuresMapper.OperatorControllerUpstreamForDownstream,
			)
		case catalogdDeploymentName:
			upstreamEnabledGates, upstreamDisabledGates = upstreamFeatureGates(
				clusterGatesConfig,
				featuresMapper.UpstreamDefaults("catalogd"),
				featuresMapper.CatalogdDownstreamFeatureGates(),
				featuresMapper.CatalogdUpstreamForDownstream,
			)
		default:
			logger.V(4).Info("unrecognized deployment", "deployment", deployment.Name)
			return nil
		}
		logger.V(4).Info("enabled feature gates", "enabled feature gates", upstreamEnabledGates, "disabled feature gates", upstreamDisabledGates, "deployment", deployment.Name)

		argToSet := internalfeatures.FormatAsFeatureGateArgs(upstreamEnabledGates, upstreamDisabledGates)
		var errs []error
		for i := range deployment.Spec.Template.Spec.Containers {
			logger.V(4).Info("iterating containers", "container", deployment.Spec.Template.Spec.Containers[i].Name, "deployment", deployment.Name)
			if !strings.EqualFold(deployment.Spec.Template.Spec.Containers[i].Name, "manager") {
				continue
			}
			err = setContainerArg(&deployment.Spec.Template.Spec.Containers[i], "--feature-gates", argToSet)
			if err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}

		return nil
	}
}

// upstreamFeatureGates build and returns a unique and ordered list of upstream feature gates names
// that map to the provided downstream feature gates
func upstreamFeatureGates(
	clusterGatesConfig featuregates.FeatureGate,
	upstreamDefaultsConfig featuregates.FeatureGate,
	downstreamGates []configv1.FeatureGateName,
	downstreamToUpstreamFunc func(configv1.FeatureGateName) []string,
) (enabled, disabled []string) {
	var upstreamEnabledGates, upstreamDisabledGates []string

	seen := make(map[string]struct{})
	for _, downstreamGate := range downstreamGates {
		if !clusterGatesConfig.Enabled(downstreamGate) {
			continue
		}

		for _, upstreamGate := range downstreamToUpstreamFunc(downstreamGate) {
			if _, found := seen[upstreamGate]; found {
				continue
			}

			seen[upstreamGate] = struct{}{}
			if !clusterGatesConfig.Enabled(downstreamGate) && upstreamDefaultsConfig.Enabled(configv1.FeatureGateName(upstreamGate)) {
				//enabled by default upstream but not downstream
				upstreamDisabledGates = append(upstreamDisabledGates, upstreamGate)
			} else if clusterGatesConfig.Enabled(downstreamGate) {
				upstreamEnabledGates = append(upstreamEnabledGates, upstreamGate)
			}
		}
	}
	for _, upstreamGate := range upstreamDefaultsConfig.KnownFeatures() {
		if _, found := seen[string(upstreamGate)]; found {
			continue
		}
		if upstreamDefaultsConfig.Enabled(upstreamGate) {
			// upstream feature gate on by default with no downstream mapping
			upstreamDisabledGates = append(upstreamDisabledGates, string(upstreamGate))
		}
	}
	slices.Sort(upstreamEnabledGates)
	slices.Sort(upstreamDisabledGates)

	return upstreamEnabledGates, upstreamDisabledGates
}
