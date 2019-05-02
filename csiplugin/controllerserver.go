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
	"fmt"
	"path"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pborman/uuid"
)

type ScaleControllerServer struct {
	Driver          *ScaleDriver
}

const (
	oneGB = 1073741824
)


func (cs *ScaleControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	glog.V(3).Infof("create volume req: %v", req)
	if err := cs.Driver.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		glog.V(3).Infof("invalid create volume req: %v", req)
		return nil, status.Error(codes.Internal, fmt.Sprintf("CreateVolume ValidateControllerServiceRequest failed: %v", err))
	}
	// Check sanity of request Name, Volume Capabilities
	if len(req.Name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume Name cannot be empty")
	}
	if req.VolumeCapabilities == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capabilities cannot be empty")
	}

	// Need to check for already existing volume name, and if found
	// check for the requested capacity and already allocated capacity
	if exVol, err := getScaleVolumeByName(req.GetName()); err == nil {
		glog.V(3).Infof("volume %s already exists: %s", req.GetName())
		// Since err is nil, it means the volume with the same name already exists
		// need to check if the size of exisiting volume is the same as in new
		// request
		if exVol.VolSize >= int64(req.GetCapacityRange().GetRequiredBytes()) {
			// exisiting volume is compatible with new request and should be reused.
			// TODO Do I need to make sure that volume still exists?
			return &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					VolumeId:            exVol.VolID,
					CapacityBytes: int64(exVol.VolSize),
					VolumeContext:    req.GetParameters(),
				},
			}, nil
		}
		return nil, status.Error(codes.AlreadyExists,
					 fmt.Sprintf("Volume with the same name: %s but with different size already exist",
						     req.GetName()))
	}
	glog.V(3).Infof("volume with name %s does not exist (volumes len: %d), create\n", req.GetName(), len(scaleVolumes))

	scaleVol, err := getScaleVolumeOptions(req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.Internal,
					 fmt.Sprintf("CreateVolume unable to get volume options: %v", err))
	}

	/* Generating Volume Name and Volume ID, as according to CSI
	 * spec they MUST be different. */
	volName := req.GetName()
	uniqueID := uuid.NewUUID().String()
	if len(volName) == 0 {
		volName = "dynamic_pvc_" + uniqueID
	}
	scaleVol.VolName = volName
	//volumeID := "csi_scale-" + uniqueID
	volumeID := uniqueID
	if (len(scaleVol.VolBackendFs) > 0) {
		volumeID += "-fileset"
	}
	scaleVol.VolID = volumeID
	// Volume Size - Default is 1 GiB
	volSizeBytes := int64(oneGB)
	if req.GetCapacityRange() != nil {
		volSizeBytes = int64(req.GetCapacityRange().GetRequiredBytes())
	}
	scaleVol.VolSize = volSizeBytes
	volSizeMB := int(volSizeBytes / 1024 / 1024)

	if err := createScaleImage(scaleVol, volSizeMB); err != nil {
		if err != nil {
			glog.Warningf("failed to create volume: %v", err)
			return nil, status.Error(codes.Internal,
						 fmt.Sprintf("Failed to create volume: %v", err))
		}
	}
	glog.V(4).Infof("created scale backend volume %s, create response", volName)
	// Storing volInfo into a persistent file.
	if err := persistVolInfo(volumeID, path.Join(PluginFolder, "controller"), scaleVol); err != nil {
		glog.Warningf("failed to store volInfo with error: %v", err)
		return nil, status.Error(codes.Internal,
					 fmt.Sprintf("failed to store volInfo with error: %v", err))
	}
	scaleVolumes[volumeID] = scaleVol
	glog.V(4).Infof("Added volumeID %s in scaleVolumes (len %d)", volumeID, len(scaleVolumes))
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:            volumeID,
			CapacityBytes: int64(volSizeBytes),
			VolumeContext:    req.GetParameters(),
		},
	}, nil
}

func (cs *ScaleControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if err := cs.Driver.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		glog.Warningf("invalid delete volume req: %v", req)
		return nil, status.Error(codes.InvalidArgument,
					 fmt.Sprintf("invalid delete volume req (%v): %v", req, err))
	}
	// For now the image get unconditionally deleted, but here retention policy can be checked
	volumeID := req.GetVolumeId()
	scaleVol := &scaleVolume{}
	if err := loadVolInfo(volumeID, path.Join(PluginFolder, "controller"), scaleVol); err != nil {
		return nil, status.Error(codes.Internal,
					 fmt.Sprintf("failed to load volInfo: %v", err))
	}
	volName := scaleVol.VolName
	// Deleting scale image
	glog.V(4).Infof("deleting volume %s", volName)
	if err := deleteScaleImage(scaleVol); err != nil {
		glog.V(3).Infof("failed to delete scale image: %s with error: %v", volName, err)
		return nil, status.Error(codes.Internal,
					 fmt.Sprintf("failed to delete scale image: %s with error: %v", volName, err))
	}
	// Removing persistent storage file for the unmapped volume
	if err := deleteVolInfo(volumeID, path.Join(PluginFolder, "controller")); err != nil {
		return nil, status.Error(codes.Internal,
					 fmt.Sprintf("failed to delete volInfo with error: %v", err))
	}

	delete(scaleVolumes, volumeID)
	return &csi.DeleteVolumeResponse{}, nil
}

// ControllerGetCapabilities implements the default GRPC callout.
func (cs *ScaleControllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	glog.V(4).Infof("ControllerGetCapabilities called with req: %#v", req)
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: cs.Driver.cscap,
	}, nil
}

func (cs *ScaleControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	for _, cap := range req.VolumeCapabilities {
		if cap.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: ""}, nil
		}
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.VolumeCapabilities,
		},
	}, nil
}

func (cs *ScaleControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (cs *ScaleControllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return &csi.ControllerPublishVolumeResponse{}, nil
}

func (cs *ScaleControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ScaleControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ScaleControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ScaleControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (cs *ScaleControllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (cs *ScaleControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
        return nil, status.Error(codes.Unimplemented, "")
}
