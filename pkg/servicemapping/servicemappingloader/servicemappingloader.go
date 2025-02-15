// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package servicemappingloader

import (
	"fmt"
	"io"
	"io/ioutil"
	"path"

	"github.com/GoogleCloudPlatform/k8s-config-connector/pkg/apis/core/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-config-connector/pkg/gcp"
	"github.com/GoogleCloudPlatform/k8s-config-connector/pkg/servicemapping/embed"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ghodss/yaml"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ServiceMappingLoader struct {
	groupToSM           map[string]v1alpha1.ServiceMapping
	serviceHostNameToSM map[string]v1alpha1.ServiceMapping
}

func New() (*ServiceMappingLoader, error) {
	serviceMappings, err := GetServiceMappings()
	if err != nil {
		return nil, fmt.Errorf("error loading service mappings: %v", err)
	}
	return NewFromServiceMappings(serviceMappings), nil
}

func NewFromServiceMappings(serviceMappings []v1alpha1.ServiceMapping) *ServiceMappingLoader {
	groupToSM := make(map[string]v1alpha1.ServiceMapping)
	serviceHostNameToSM := make(map[string]v1alpha1.ServiceMapping)
	for _, sm := range serviceMappings {
		groupToSM[sm.Name] = sm
		serviceHostNameToSM[sm.Spec.ServiceHostName] = sm
	}
	loader := ServiceMappingLoader{
		groupToSM:           groupToSM,
		serviceHostNameToSM: serviceHostNameToSM,
	}
	return &loader
}

func (s *ServiceMappingLoader) GetServiceMapping(name string) (*v1alpha1.ServiceMapping, error) {
	sm, ok := s.groupToSM[name]
	if !ok {
		return nil, fmt.Errorf("unable to get service mapping: no mapping with name '%v' found", name)
	}
	return &sm, nil
}

func (s *ServiceMappingLoader) GetServiceMappingForServiceHostName(hostName string) (*v1alpha1.ServiceMapping, error) {
	sm, ok := s.serviceHostNameToSM[hostName]
	if !ok {
		return nil, fmt.Errorf("unable to get service mapping: no mapping with service host name '%v' found", hostName)
	}
	return &sm, nil
}

func (s *ServiceMappingLoader) GetServiceMappings() []v1alpha1.ServiceMapping {
	serviceMappings := make([]v1alpha1.ServiceMapping, 0, len(s.groupToSM))
	for _, v := range s.groupToSM {
		serviceMappings = append(serviceMappings, v)
	}
	return serviceMappings
}

func (s *ServiceMappingLoader) GetResourceConfig(u *unstructured.Unstructured) (*v1alpha1.ResourceConfig, error) {
	sm, err := s.GetServiceMapping(u.GroupVersionKind().Group)
	if err != nil {
		return nil, fmt.Errorf("error getting service mapping: %v", err)
	}
	return GetResourceConfig(sm, u)
}

// a single GVK can be associated with multiple ResourceConfigs because some resources have both Global and Regional ResourceConfigs
func (s *ServiceMappingLoader) GetResourceConfigs(gvk schema.GroupVersionKind) ([]*v1alpha1.ResourceConfig, error) {
	sm, err := s.GetServiceMapping(gvk.Group)
	if err != nil {
		return nil, fmt.Errorf("error getting service mapping for group '%v': %v", gvk.Group, err)
	}
	return GetResourceConfigsForKind(sm, gvk.Kind), nil
}

func GetResourceConfig(sm *v1alpha1.ServiceMapping, u *unstructured.Unstructured) (*v1alpha1.ResourceConfig, error) {
	rcs := GetResourceConfigsForKind(sm, u.GetKind())
	if len(rcs) == 0 {
		return nil, fmt.Errorf("couldn't find any ResourceConfig defined for kind %v", u.GetKind())
	}
	if len(rcs) == 1 {
		return rcs[0], nil
	}

	// Special scenario: Compute Instance
	if u.GetKind() == "ComputeInstance" {
		return getComputeInstanceTFResource(u, rcs)
	}

	l, err := getLocationalityOfResource(u)
	if err != nil {
		return nil, fmt.Errorf("couldn't find the right ResourceConfig for Kind %v: %v", u.GetKind(), err)
	}
	for _, rc := range rcs {
		if rc.Locationality == l {
			return rc, nil
		}
	}
	return nil, fmt.Errorf("couldn't find the right ResourceConfig for Kind %v given the locationality of the resource - %v: %v", u.GetKind(), l, err)
}

func GetResourceConfigsForKind(sm *v1alpha1.ServiceMapping, kind string) []*v1alpha1.ResourceConfig {
	rcs := make([]*v1alpha1.ResourceConfig, 0)
	for _, r := range sm.Spec.Resources {
		r := r
		if r.Kind == kind {
			rcs = append(rcs, &r)
		}
	}
	return rcs
}

func getLocationalityOfResource(u *unstructured.Unstructured) (string, error) {
	location, found, err := unstructured.NestedString(u.Object, "spec", "location")
	if err != nil || !found {
		return "", fmt.Errorf("couldn't find location field: %v", err)
	}

	if location == gcp.Global {
		return gcp.Global, nil
	}

	if gcp.IsLocationRegional(location) {
		return gcp.Regional, nil
	}

	if gcp.IsLocationZonal(location) {
		return gcp.Zonal, nil
	}
	return "", fmt.Errorf("location is neither global nor regional nor zonal")
}

func GetServiceMappings() ([]v1alpha1.ServiceMapping, error) {
	baseDirName := "/"
	smDir, err := embed.Assets.Open(baseDirName)
	if err != nil {
		return nil, fmt.Errorf("error reading files in ServiceMapping directory: %v", err)
	}
	defer smDir.Close()
	files, err := smDir.Readdir(0)
	if err != nil {
		return nil, fmt.Errorf("error reading files in ServiceMapping directory: %v", err)
	}
	serviceMappings := make([]v1alpha1.ServiceMapping, 0)
	for _, file := range files {
		smPath := path.Join(baseDirName, file.Name())
		sm, err := fileToServiceMapping(smPath)
		if err != nil {
			return nil, err
		}
		serviceMappings = append(serviceMappings, *sm)
	}
	return serviceMappings, nil
}

func fileToServiceMapping(filePath string) (*v1alpha1.ServiceMapping, error) {
	file, err := embed.Assets.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("error opening file '%v': %v", filePath, err)
	}
	defer file.Close()
	sm, err := readerToServiceMapping(file)
	if err != nil {
		return nil, fmt.Errorf("error reading file '%v' to service mapping: %v", filePath, err)
	}
	return sm, nil
}

func readerToServiceMapping(r io.Reader) (*v1alpha1.ServiceMapping, error) {
	bytes, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("error reading file: %v", err)
	}
	var sm v1alpha1.ServiceMapping
	if err := yaml.Unmarshal(bytes, &sm); err != nil {
		return nil, fmt.Errorf("error unmarshaling byte to service mapping: %v", err)
	}
	return &sm, nil
}

func getComputeInstanceTFResource(u *unstructured.Unstructured, rcs []*v1alpha1.ResourceConfig) (*v1alpha1.ResourceConfig, error) {
	// If the configuration defines the instanceTemplateRef, use TF resource compute_instance_from_template.
	// Otherwise, use TF resource compute_instance.
	_, hasTemplate, err := unstructured.NestedMap(u.Object, "spec", "instanceTemplateRef")
	if err != nil {
		return nil, fmt.Errorf("instanceTemplateRef should be a map in Kind %v: %v", u.GetKind(), err)
	}
	for _, rc := range rcs {
		if rc.Name == "google_compute_instance_from_template" && hasTemplate {
			return rc, nil
		}
		if rc.Name == "google_compute_instance" && !hasTemplate {
			return rc, nil
		}
	}

	return nil, fmt.Errorf("couldn't find the right ResourceConfig for Kind %v: %v", u.GetKind(), err)
}
