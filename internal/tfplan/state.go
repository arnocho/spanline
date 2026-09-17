package tfplan

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
)

// StateInstance is one instance of a resource in a state file. IndexKey is the count index
// (a number) or the for_each key (a string) of the instance, absent on a single instance.
type StateInstance struct {
	IndexKey   any            `json:"index_key,omitempty"`
	Attributes map[string]any `json:"attributes"`
}

// StateResource is one resource block of a state file. Module is the module path the block
// lives in, such as module.aks, empty at the root.
type StateResource struct {
	Module    string          `json:"module,omitempty"`
	Mode      string          `json:"mode"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Provider  string          `json:"provider"`
	Instances []StateInstance `json:"instances"`
}

// Address is the Terraform address of the resource block, module path included, as an
// operator would type it.
func (r StateResource) Address() string {
	if r.Module != "" {
		return r.Module + "." + r.Type + "." + r.Name
	}
	return r.Type + "." + r.Name
}

// InstanceAddress is the address of one instance of the block: the block address followed by
// the count index or the for_each key, so a pool made with for_each is named exactly.
func (r StateResource) InstanceAddress(inst StateInstance) string {
	switch k := inst.IndexKey.(type) {
	case nil:
		return r.Address()
	case string:
		return r.Address() + `["` + k + `"]`
	case float64:
		return r.Address() + "[" + strconv.FormatFloat(k, 'f', -1, 64) + "]"
	default:
		return fmt.Sprintf("%s[%v]", r.Address(), k)
	}
}

// State is the subset of a Terraform or OpenTofu state file that spanline reads.
// Only identity is used: address, type, name and the resource's own name attribute.
// Attribute values are never printed, so a state holding secrets is not disclosed.
type State struct {
	Version          int             `json:"version"`
	TerraformVersion string          `json:"terraform_version"`
	Serial           int             `json:"serial"`
	Lineage          string          `json:"lineage"`
	Resources        []StateResource `json:"resources"`
	Path             string          `json:"-"`
}

// ParseState decodes a state file.
func ParseState(b []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("this file is not a Terraform or OpenTofu state: %w", err)
	}
	if s.Version == 0 && s.Resources == nil {
		return nil, fmt.Errorf("this JSON has no version and no resources, so it is not a state file")
	}
	return &s, nil
}

// LoadStateFile reads and decodes a state file from disk.
func LoadStateFile(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := ParseState(b)
	if err != nil {
		return nil, err
	}
	s.Path = path
	return s, nil
}

// Owner ties a live object back to the Terraform address that declares it.
type Owner struct {
	Name      string
	Address   string
	StateFile string
	Type      string
}

// NodePools lists the node pools this state declares, by pool name.
func (s *State) NodePools() []Owner {
	var out []Owner
	for _, r := range s.Resources {
		nameAttr, ok := nodePoolTypes[r.Type]
		if !ok || r.Mode != "managed" {
			continue
		}
		for _, inst := range r.Instances {
			name := attrString(inst.Attributes, nameAttr)
			if name == "" {
				continue
			}
			out = append(out, Owner{Name: name, Address: r.InstanceAddress(inst), StateFile: s.Path, Type: r.Type})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Clusters lists the clusters this state declares, by cluster name.
func (s *State) Clusters() []Owner {
	var out []Owner
	for _, r := range s.Resources {
		nameAttr, ok := clusterTypes[r.Type]
		if !ok || r.Mode != "managed" {
			continue
		}
		for _, inst := range r.Instances {
			name := attrString(inst.Attributes, nameAttr)
			if name == "" {
				continue
			}
			out = append(out, Owner{Name: name, Address: r.InstanceAddress(inst), StateFile: s.Path, Type: r.Type})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HelmReleases lists helm_release resources, which is how Terraform often owns workloads.
func (s *State) HelmReleases() []Owner {
	var out []Owner
	for _, r := range s.Resources {
		if r.Type != "helm_release" || r.Mode != "managed" {
			continue
		}
		for _, inst := range r.Instances {
			name := attrString(inst.Attributes, "name")
			ns := attrString(inst.Attributes, "namespace")
			if name == "" {
				continue
			}
			out = append(out, Owner{Name: ns + "/" + name, Address: r.InstanceAddress(inst), StateFile: s.Path, Type: r.Type})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CountResources counts managed resources, for the state summary line.
func (s *State) CountResources() int {
	n := 0
	for _, r := range s.Resources {
		if r.Mode == "managed" {
			n += len(r.Instances)
		}
	}
	return n
}
