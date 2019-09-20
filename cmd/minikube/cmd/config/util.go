/*
Copyright 2016 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/minikube/pkg/minikube/assets"
	"k8s.io/minikube/pkg/minikube/cluster"
	"k8s.io/minikube/pkg/minikube/command"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/constants"
	"k8s.io/minikube/pkg/minikube/exit"
	"k8s.io/minikube/pkg/minikube/machine"
	"k8s.io/minikube/pkg/minikube/out"
	"k8s.io/minikube/pkg/minikube/storageclass"
)

// Runs all the validation or callback functions and collects errors
func run(name string, value string, fns []setFn) error {
	var errors []error
	for _, fn := range fns {
		err := fn(name, value)
		if err != nil {
			errors = append(errors, err)
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("%v", errors)
	}
	return nil
}

func findSetting(name string) (Setting, error) {
	for _, s := range settings {
		if name == s.name {
			return s, nil
		}
	}
	return Setting{}, fmt.Errorf("property name %q not found", name)
}

// Set Functions

// SetString sets a string value
func SetString(m config.MinikubeConfig, name string, val string) error {
	m[name] = val
	return nil
}

// SetMap sets a map value
func SetMap(m config.MinikubeConfig, name string, val map[string]interface{}) error {
	m[name] = val
	return nil
}

// SetConfigMap sets a config map value
func SetConfigMap(m config.MinikubeConfig, name string, val string) error {
	list := strings.Split(val, ",")
	v := make(map[string]interface{})
	for _, s := range list {
		v[s] = nil
	}
	m[name] = v
	return nil
}

// SetInt sets an int value
func SetInt(m config.MinikubeConfig, name string, val string) error {
	i, err := strconv.Atoi(val)
	if err != nil {
		return err
	}
	m[name] = i
	return nil
}

// SetBool sets a bool value
func SetBool(m config.MinikubeConfig, name string, val string) error {
	b, err := strconv.ParseBool(val)
	if err != nil {
		return err
	}
	m[name] = b
	return nil
}

// EnableOrDisableAddon updates addon status executing any commands necessary
func EnableOrDisableAddon(name string, val string) error {
	enable, err := strconv.ParseBool(val)
	if err != nil {
		return errors.Wrapf(err, "parsing bool: %s", name)
	}

	// TODO(r2d4): config package should not reference API, pull this out
	api, err := machine.NewAPIClient()
	if err != nil {
		return errors.Wrap(err, "machine client")
	}
	defer api.Close()
	cluster.EnsureMinikubeRunningOrExit(api, 0)

	addon := assets.Addons[name]
	host, err := cluster.CheckIfHostExistsAndLoad(api, config.GetMachineName())
	if err != nil {
		return errors.Wrap(err, "getting host")
	}
	cmd, err := machine.CommandRunner(host)
	if err != nil {
		return errors.Wrap(err, "command runner")
	}

	cfg, err := config.Load()
	if err != nil && !os.IsNotExist(err) {
		exit.WithCodeT(exit.Data, "Unable to load config: {{.error}}", out.V{"error": err})
	}

	data := assets.GenerateTemplateData(cfg.KubernetesConfig)
	return enableOrDisableAddonInternal(addon, cmd, data, enable)
}

func isAddonAlreadySet(addon *assets.Addon, enable bool) error {

	addonStatus, err := addon.IsEnabled()

	if err != nil {
		return errors.Wrap(err, "get the addon status")
	}

	if addonStatus && enable {
		return fmt.Errorf("addon %s was already enabled", addon.Name())
	} else if !addonStatus && !enable {
		return fmt.Errorf("addon %s was already disabled", addon.Name())
	}

	return nil
}

func enableOrDisableAddonInternal(addon *assets.Addon, cmd command.Runner, data interface{}, enable bool) error {
	var err error
	// check addon status before enabling/disabling it
	if err := isAddonAlreadySet(addon, enable); err != nil {
		out.ErrT(out.Conflict, "{{.error}}", out.V{"error": err})
		os.Exit(0)
	}

	if enable {
		for _, addon := range addon.Assets {
			var addonFile assets.CopyableFile
			if addon.IsTemplate() {
				addonFile, err = addon.Evaluate(data)
				if err != nil {
					return errors.Wrapf(err, "evaluate bundled addon %s asset", addon.GetAssetName())
				}

			} else {
				addonFile = addon
			}
			if err := cmd.Copy(addonFile); err != nil {
				return errors.Wrapf(err, "enabling addon %s", addon.AssetName)
			}
		}
	} else {
		for _, addon := range addon.Assets {
			var addonFile assets.CopyableFile
			if addon.IsTemplate() {
				addonFile, err = addon.Evaluate(data)
				if err != nil {
					return errors.Wrapf(err, "evaluate bundled addon %s asset", addon.GetAssetName())
				}

			} else {
				addonFile = addon
			}
			if err := cmd.Remove(addonFile); err != nil {
				return errors.Wrapf(err, "disabling addon %s", addon.AssetName)
			}
		}
	}
	return nil
}

// EnableOrDisableStorageClasses enables or disables storage classes
func EnableOrDisableStorageClasses(name, val string) error {
	enable, err := strconv.ParseBool(val)
	if err != nil {
		return errors.Wrap(err, "Error parsing boolean")
	}

	class := constants.DefaultStorageClassProvisioner
	if name == "storage-provisioner-gluster" {
		class = "glusterfile"
	}
	storagev1, err := storageclass.GetStoragev1()
	if err != nil {
		return errors.Wrapf(err, "Error getting storagev1 interface %v ", err)
	}

	if enable {
		// Only StorageClass for 'name' should be marked as default
		err = storageclass.SetDefaultStorageClass(storagev1, class)
		if err != nil {
			return errors.Wrapf(err, "Error making %s the default storage class", class)
		}
	} else {
		// Unset the StorageClass as default
		err := storageclass.DisableDefaultStorageClass(storagev1, class)
		if err != nil {
			return errors.Wrapf(err, "Error disabling %s as the default storage class", class)
		}
	}

	return EnableOrDisableAddon(name, val)
}
