/**
 * Copyright 2019 IBM Corp.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package settings

import (
        "fmt"
        "strings"
	"path"
        "io/ioutil"
        "encoding/json"

        "github.com/golang/glog"
)

type ScaleSettingsConfigMap struct {
    Clusters []Clusters
}

type Primary struct { 
    PrimaryFS string `json:"primaryFS"` 
    PrimaryFset string `json:"primaryFset"` 
    PrimaryCid  string `json:"primaryCid"`
    InodeLimit  string `json:"inode-limit"`
    RemoteCluster string `json:"remoteCluster"`
    RemoteFS string `json:"remoteFS"`

    PrimaryFSMount string
    PrimaryFsetLink string
    SymlinkAbsolutePath string
    SymlinkRelativePath string
} 

type RestAPI struct { 
    GuiHost string `json:"guiHost"` 
    GuiPort int `json:"guiPort"` 
} 

type Clusters struct { 
    ID string `json:"id"` 
    Primary Primary `json:"primary,omitempty"` 
    SecureSslMode bool `json:"secureSslMode"` 
    Cacert string `json:"cacert"` 
    Secrets string `json:"secrets"` 
    RestAPI []RestAPI `json:"restApi"` 

    MgmtUsername string
    MgmtPassword string
    CacertValue []byte
}

const ( 
        DefaultGuiPort int = 443
        GuiProtocol string = "https"
        ConfigMapFile string = "/var/lib/ibm/config/spectrum-scale-config.json"
	SecretBasePath string = "/var/lib/ibm/"
	CertificatePath string = "/var/lib/ibm/ssl/public"
)

func LoadScaleConfigSettings() ScaleSettingsConfigMap {
	glog.V(5).Infof("scale_config LoadScaleConfigSettings")

        file, e := ioutil.ReadFile(ConfigMapFile) // TODO
        if e != nil {
                glog.Errorf("Spectrum Scale configuration not found: %v", e)
                return ScaleSettingsConfigMap{}
        }
        cmsj := &ScaleSettingsConfigMap{}
        e = json.Unmarshal(file, cmsj) 
        if e != nil {
                glog.Errorf("Error in unmarshalling Spectrum Scale configuration json: %v", e)
                return ScaleSettingsConfigMap{}
	}

        e = HandleSecretsAndCerts(cmsj)
        if e != nil {
                glog.Errorf("Error in secrets or certificates: %v", e)
                return ScaleSettingsConfigMap{}
        }
        return *cmsj
}

func HandleSecretsAndCerts(cmap *ScaleSettingsConfigMap) error {
        glog.V(5).Infof("scale_config HandleSecrets")
        for i := 0; i < len(cmap.Clusters); i++ {
            if cmap.Clusters[i].Secrets != "" {
		unamePath := path.Join(SecretBasePath, cmap.Clusters[i].Secrets, "username")
                file, e := ioutil.ReadFile(unamePath)
                if e != nil {
                    return fmt.Errorf("Spectrum Scale secret not found: %v\n", e)
                }
		file_s := string(file)
		file_s = strings.TrimSpace(file_s)
		file_s = strings.TrimSuffix(file_s, "\n")
		cmap.Clusters[i].MgmtUsername = string(file_s)

		pwdPath := path.Join(SecretBasePath, cmap.Clusters[i].Secrets, "password")
                file, e = ioutil.ReadFile(pwdPath)
                if e != nil {
                    return fmt.Errorf("Spectrum Scale secret not found: %v\n", e)
                }  
                file_s = string(file)
                file_s = strings.TrimSpace(file_s) 
		file_s = strings.TrimSuffix(file_s, "\n") 
		cmap.Clusters[i].MgmtPassword = string(file_s)
            }

	    if cmap.Clusters[i].SecureSslMode && cmap.Clusters[i].Cacert != "" {
		certPath := path.Join(CertificatePath, cmap.Clusters[i].Cacert)
                file, e := ioutil.ReadFile(certPath)
                if e != nil {
                    return fmt.Errorf("Spectrum Scale CA certificate not found: %v\n", e)
                }
                cmap.Clusters[i].CacertValue = file
	    }
        }
        return nil
}

