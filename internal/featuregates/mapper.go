package featuregates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"
)

// Add your new upstream feature gate here
// const (
// 		MyUpstreamFeature = "MyUpstreamFeature"
// )

type MapperInterface interface {
	OperatorControllerUpstreamForDownstream(downstreamGate configv1.FeatureGateName) []string
	OperatorControllerDownstreamFeatureGates() []configv1.FeatureGateName
	CatalogdUpstreamForDownstream(downstreamGate configv1.FeatureGateName) []string
	CatalogdDownstreamFeatureGates() []configv1.FeatureGateName
	UpstreamDefaults(componentName string) featuregates.FeatureGate
}

// Mapper knows the mapping between downstream and upstream feature gates for both OLM components
type Mapper struct {
	componentGates   map[string]map[configv1.FeatureGateName][]string
	upstreamDefaults map[string]featuregates.FeatureGate
}

func NewMapper(assetMetadataDir string) *Mapper {
	// Add your downstream to upstream mapping here
	componentGates := map[string]map[configv1.FeatureGateName][]string{
		"catalogd": {
			configv1.FeatureGateName("NewOLM"): {"OnUpstream", "OffUpstream"},
			// features.FeatureGateNewOLMMyDownstreamFeature: {MyUpstreamCatalogdFeature}
		},
		"operator-controller": {
			configv1.FeatureGateName("NewOLM"): {"OnUpstream", "OffUpstream"},
			// features.FeatureGateNewOLMMyDownstreamFeature: {MyUpstreamControllerOperatorFeature}
		},
	}

	for _, m := range componentGates {
		for downstreamGate := range m {
			// features.FeatureGateNewOLM is a GA-enabled downstream feature gate.
			// If there is a need to enable upstream alpha/beta features in the downstream GA release
			// get approval via a merged openshift/enhancement describing the need, then carve out
			// an exception in this failsafe code
			if downstreamGate == features.FeatureGateNewOLM {
				panic(errors.New("FeatureGateNewOLM used in mappings"))
			}
			if !strings.HasPrefix(string(downstreamGate), string(features.FeatureGateNewOLM)) {
				panic(errors.New("all downstream feature gates must use NewOLM prefix by convention"))
			}
		}
	}

	mapper := &Mapper{componentGates: componentGates, upstreamDefaults: map[string]featuregates.FeatureGate{}}
	if len(assetMetadataDir) > 0 {
		for componentName := range componentGates {
			enabledGates := []configv1.FeatureGateName{}
			disabledGates := []configv1.FeatureGateName{}
			upstreamGateInfo, err := loadUpstreamGates(filepath.Join(assetMetadataDir, fmt.Sprintf("%s.json", componentName)))
			if err != nil {
				klog.FromContext(context.Background()).WithName("builder").V(0).Info("error reading upstream feature gate metadata file, using internal featuregate mapping instead", "component", componentName, "error", err)
			} else {
				for _, fgInfo := range upstreamGateInfo {
					if fgInfo.Enabled {
						enabledGates = append(enabledGates, configv1.FeatureGateName(fgInfo.Name))
					} else {
						disabledGates = append(disabledGates, configv1.FeatureGateName(fgInfo.Name))
					}
				}
			}
			mapper.upstreamDefaults[componentName] = featuregates.NewFeatureGate(enabledGates, disabledGates)
		}
	}
	return mapper
}

// OperatorControllerDownstreamFeatureGates returns a list of all downstream feature gates
// which have an upstream mapping configured for the operator-controller component
func (m *Mapper) OperatorControllerDownstreamFeatureGates() []configv1.FeatureGateName {
	return getKeys(m.componentGates["operator-controller"])
}

// CatalogdDownstreamFeatureGates returns a list of all downstream feature gates
// which have an upstream mapping configured for the catalogd component
func (m *Mapper) CatalogdDownstreamFeatureGates() []configv1.FeatureGateName {
	return getKeys(m.componentGates["catalogd"])
}

// OperatorControllerUpstreamForDownstream returns upstream feature gates which are configured
// for a given downstream feature gate for the operator-controller component
func (m *Mapper) OperatorControllerUpstreamForDownstream(downstreamGate configv1.FeatureGateName) []string {
	if fg, ok := m.componentGates["operator-controller"][downstreamGate]; ok {
		return fg
	}
	// no explicit mapping, make a best guess about corresponding upstream feature gate name
	for _, fg := range m.upstreamDefaults["operator-controller"].KnownFeatures() {
		if string(fg) == strings.TrimPrefix(fmt.Sprintf("%s.", string(downstreamGate)), string(features.FeatureGateNewOLM)) {
			return []string{string(fg)}
		}
	}
	return nil
}

// CatalogdUpstreamForDownstream returns upstream feature gates which are configured
// for a given downstream feature gate for the catalogd component
func (m *Mapper) CatalogdUpstreamForDownstream(downstreamGate configv1.FeatureGateName) []string {
	if fg, ok := m.componentGates["catalogd"][downstreamGate]; ok {
		return fg
	}
	// no explicit mapping, make a best guess about corresponding upstream feature gate name
	for _, fg := range m.upstreamDefaults["catalogd"].KnownFeatures() {
		if string(fg) == strings.TrimPrefix(fmt.Sprintf("%s.", string(downstreamGate)), string(features.FeatureGateNewOLM)) {
			return []string{string(fg)}
		}
	}
	return nil
}

// UpstreamDefaults returns the default mapping of featuregates for downstream components. This does not take into
// consideration feature flags set on the base manifest
func (m *Mapper) UpstreamDefaults(componentName string) featuregates.FeatureGate {
	if fg, ok := m.upstreamDefaults[componentName]; ok {
		return fg
	}
	return nil
}

// FormatAsFeatureGateArgs combines list of feature gate names into
// an all-enabled arg format of <feature_gate_name1>=true,<feature_gate_name1>=true etc.
func FormatAsFeatureGateArgs(enabledFeatureGates, disabledFeatureGates []string) string {
	buf := bytes.Buffer{}
	for _, gateName := range enabledFeatureGates {
		buf.WriteString(gateName)
		buf.WriteRune('=')
		buf.WriteString("true")
		buf.WriteRune(',')
	}
	for _, gateName := range disabledFeatureGates {
		buf.WriteString(gateName)
		buf.WriteRune('=')
		buf.WriteString("false")
		buf.WriteRune(',')
	}
	if buf.Len() > 0 {
		// get rid of trailing ','
		buf.Truncate(buf.Len() - 1)
	}

	return buf.String()
}

func getKeys(m map[configv1.FeatureGateName][]string) []configv1.FeatureGateName {
	keys := make([]configv1.FeatureGateName, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

type featureGateInfo struct {
	Name      featuregate.Feature
	Stability string
	Enabled   bool
}

// load upstream feature gate info from specified metadata file
func loadUpstreamGates(path string) ([]featureGateInfo, error) {
	upstreamFeatureGatesInfo := []featureGateInfo{}
	dat, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(dat, &upstreamFeatureGatesInfo)
	return upstreamFeatureGatesInfo, err
}
