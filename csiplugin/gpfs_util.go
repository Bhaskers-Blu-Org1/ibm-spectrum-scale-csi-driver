/*
Copyright 2018 The Kubernetes Authors.

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

package scale

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"bytes"
	"strconv"
	"os/exec"
	"strings"
	"github.com/golang/glog"
)

type scaleVolume struct {
	VolName       string `json:"volName"`
	VolID         string `json:"volID"`
	VolSize       int64  `json:"volSize"`
	VolIscsi      bool   `json:"volIscsi"`
	VolIscsiVid   string `json:"volIscsiVid"`
	VolBackendFs  string `json:"volBackendFs"`
}


// CreateImage creates a new volume with provision and volume options.
func createScaleImage(pOpts *scaleVolume, volSzMb int) error {
	var err error

	volName := pOpts.VolID //Name
	var device string = ""

        if (len(pOpts.VolBackendFs) > 0) {
                // Create fileset on existing filesystem
                glog.V(4).Infof("create fileset on filesystem (%s) for volume (%s)", pOpts.VolBackendFs, volName)
		s := strings.Split(pOpts.VolBackendFs, "/")
		fsName := s[len(s)-1]
                err = ops.CreateFSet(fsName, volName) // mmcrfileset gpfs1 fset1
                if err != nil {
                        glog.Errorf("Failed to create fileset: %v", err)
                        return err
                }
		err = ops.LinkFSet(fsName, volName, pOpts.VolBackendFs+"/"+volName) // mmlinkfileset gpfs1 fset1 -J /gpfs1/fset1
                if err != nil {
                        glog.Errorf("Failed to link fileset: %v", err)
                        return err
		}
                return nil
        }

	/* 
	* Check if device for NSD must be backed by iSCSI
	* and if so, create/attach an iSCSI volume.
	*/
	if (pOpts.VolIscsi) {
		// Create iSCSI volume.
		glog.V(4).Infof("create iSCSI volume (%s)", volName)
		pbVol, err := iscsiOps.CreateVolume(volName, volSzMb)
		if err != nil {
			glog.Errorf("Failed to create volume (%s): %v", volName, err)
			return err
		}
		pOpts.VolIscsiVid = pbVol.Vid.Val

		// Attach volume to all nodes in scale cluster.
		nodes := []string{"fin27p","fin31p","fin57p"} // TODO
		for _, node := range nodes {
			err = ops.AttachIscsiDevicesToNode(pbVol.Iphost, pbVol.Iqn, node)
			if err != nil {
				glog.Errorf("Failed to attach devices to node (%s): %v", node, err)
				return err
			}
		}
		device = iscsiOps.GetDevicePath(pbVol.Iphost, pbVol.Iqn, strconv.Itoa(int(pbVol.Lun)))
		// Pick node for NSD
		device = "fin31p:"+device
	} else {
	        // Pick raw device based on some policy.
		device, err = ops.Sched.PickNextRawDevice()
	        if err != nil {
			glog.Errorf("Failed to pick backing device: %v", err)
			return err
	        }
	}

	glog.V(4).Infof("create NSD (%s) backed by %s", volName, device)
	err = ops.CreateNSD(volName, device)
	if err != nil {
		glog.Errorf("Failed to create nsd (%s): %v", volName, err)
		return err
	}

	glog.V(4).Infof("create FS (%s)", volName)
	err = ops.CreateFS(volName, volName)
	if err != nil {
		glog.Errorf("Failed to create fs (%s): %v", volName, err)
		return err
	}
	return nil
}

func getScaleVolumeOptions(volOptions map[string]string) (*scaleVolume, error) {
	//var err error
	scaleVol := &scaleVolume{}
	scaleVol.VolIscsi, _ = strconv.ParseBool(volOptions["volIscsi"])
	/*if err != nil {
		return nil, fmt.Errorf("Missing required parameter volIscsi, %v", err)
	}*/
	scaleVol.VolBackendFs = volOptions["volBackendFs"]
	return scaleVol, nil
}

func getScaleVolumeByName(volName string) (*scaleVolume, error) {
	for _, scaleVol := range scaleVolumes {
		if scaleVol.VolName == volName {
			return scaleVol, nil
		}
	}
	return nil, fmt.Errorf("volume name %s does not exit in the volumes list", volName)
}

func persistVolInfo(image string, persistentStoragePath string, volInfo *scaleVolume) error {
	file := path.Join(persistentStoragePath, image+".json")
	fp, err := os.Create(file)
	if err != nil {
		glog.Errorf("scale: failed to create persistent storage file %s with error: %v\n", file, err)
		return fmt.Errorf("scale: create err %s/%s", file, err)
	}
	defer fp.Close()
	encoder := json.NewEncoder(fp)
	if err = encoder.Encode(volInfo); err != nil {
		glog.Errorf("scale: failed to encode volInfo: %+v for file: %s with error: %v\n", volInfo, file, err)
		return fmt.Errorf("scale: encode err: %v", err)
	}
	glog.Infof("scale: successfully saved volInfo: %+v into file: %s\n", volInfo, file)
	return nil
}
func loadVolInfo(image string, persistentStoragePath string, volInfo *scaleVolume) error {
	file := path.Join(persistentStoragePath, image+".json")
	fp, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("scale: open err %s/%s", file, err)
	}
	defer fp.Close()

	decoder := json.NewDecoder(fp)
	if err = decoder.Decode(volInfo); err != nil {
		return fmt.Errorf("scale: decode err: %v.", err)
	}

	return nil
}

func deleteVolInfo(image string, persistentStoragePath string) error {
	file := path.Join(persistentStoragePath, image+".json")
	glog.Infof("scale: Deleting file for Volume: %s at: %s resulting path: %+v\n", image, persistentStoragePath, file)
	err := os.Remove(file)
	if err != nil {
		if err != os.ErrNotExist {
			return fmt.Errorf("scale: error removing file: %s/%s", file, err)
		}
	}
	return nil
}

func executeCmd(command string, args []string) ([]byte, error) {
        cmd := exec.Command(command, args...)
        var stdout bytes.Buffer
        var stderr bytes.Buffer

        cmd.Stdout = &stdout
        cmd.Stderr = &stderr
        err := cmd.Run()
        stdOut := stdout.Bytes()
        return stdOut, err
}

// DeleteImage deletes a volume with provision and volume options.
func deleteScaleImage(pOpts *scaleVolume) error {
	//var output []byte
	var err error
	volId := pOpts.VolID //Name
	glog.V(4).Infof("scale: rm %s", volId)

        if (len(pOpts.VolBackendFs) > 0) {
                // Create fileset on existing filesystem
                s := strings.Split(pOpts.VolBackendFs, "/")
		fsName := s[len(s)-1]
                err = ops.UnlinkFSet(fsName, volId) // mmunlinkfileset gpfs1 fset2 -f
                if err != nil {
                        glog.Errorf("Failed to unlink scale fileset: %v", err)
                        return err
		}
                err = ops.DeleteFSet(fsName, volId) // mmdelfileset gpfs1 fset2 -f
                if err != nil {
                        glog.Errorf("Failed to delete scale fileset: %v", err)
                        return err
                }
                return nil
        }

        // Delete FS
        glog.V(4).Infof("Delete filesystem (%s)", volId)
        err = ops.DeleteFS(volId)
        if err != nil {
                return fmt.Errorf("failed to delete scale fs: %v", err)
        }

        // Delete NSD
        glog.V(4).Infof("Delete NSD (%s)", volId)
        err = ops.DeleteNSD(volId)
        if err != nil {
                return fmt.Errorf("failed to delete scale nsd: %v", err)
        }

	//glog.Errorf("failed to delete scale image: %v, command output: %s", err, string(output))
	err = iscsiOps.DeleteVolume(pOpts.VolIscsiVid)
	return err
}
