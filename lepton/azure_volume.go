package lepton

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-06-01/compute"
	"github.com/Azure/go-autorest/autorest/to"
)

// CreateVolume uploads the volume raw file and creates a disk from it
func (a *Azure) CreateVolume(config *Config, name, data, size, provider string) (NanosVolume, error) {
	var vol NanosVolume

	disksClient := a.getDisksClient()

	location := a.getLocation(config)

	sizeInt, err := strconv.Atoi(size)
	if err != nil {
		return vol, err
	}

	vol, err = CreateLocalVolume(config, name, data, size, provider)
	if err != nil {
		return vol, fmt.Errorf("create local volume: %v", err)
	}

	config.CloudConfig.ImageName = name

	err = a.Storage.CopyToBucket(config, vol.Path)
	if err != nil {
		return vol, fmt.Errorf("copy volume archive to azure bucket: %v", err)
	}

	bucket := config.CloudConfig.BucketName
	if bucket == "" {
		bucket = a.storageAccount
	}
	container := "quickstart-nanos"
	disk := name + ".vhd"

	sourceURI := "https://" + bucket + ".blob.core.windows.net/" + container + "/" + disk

	diskParams := compute.Disk{
		Location: to.StringPtr(location),
		Name:     to.StringPtr(name),
		DiskProperties: &compute.DiskProperties{
			HyperVGeneration: compute.V1,
			DiskSizeGB:       to.Int32Ptr(int32(sizeInt / 1000 / 1000)),
			CreationData: &compute.CreationData{
				CreateOption:     "Import",
				SourceURI:        to.StringPtr(sourceURI),
				StorageAccountID: to.StringPtr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", a.subID, a.groupName, bucket)),
			},
		},
	}

	_, err = disksClient.CreateOrUpdate(context.TODO(), a.groupName, name, diskParams)
	if err != nil {
		return vol, err
	}

	return vol, nil
}

// GetAllVolumes returns all volumes in NanosVolume format
func (a *Azure) GetAllVolumes(config *Config) (*[]NanosVolume, error) {
	vols := &[]NanosVolume{}

	volumesService := a.getDisksClient()

	azureDisksPage, err := volumesService.List(context.TODO())
	if err != nil {
		return nil, err
	}

	for {
		disks := azureDisksPage.Values()

		if disks == nil {
			break
		}

		for _, disk := range disks {
			var attachedTo string
			if disk.ManagedBy != nil {
				instanceURLParts := strings.Split(*disk.ManagedBy, "/")

				attachedTo = instanceURLParts[len(instanceURLParts)-1]
			}

			vol := NanosVolume{
				Name:       *disk.Name,
				Status:     string(disk.DiskProperties.DiskState),
				Size:       strconv.Itoa(int(*disk.DiskSizeGB)),
				Path:       "",
				CreatedAt:  disk.TimeCreated.String(),
				AttachedTo: attachedTo,
			}

			*vols = append(*vols, vol)

			azureDisksPage.Next()
		}
	}

	return vols, nil
}

// DeleteVolume deletes an existing volume
func (a *Azure) DeleteVolume(config *Config, name string) error {
	volumesService := a.getDisksClient()

	_, err := volumesService.Delete(context.TODO(), a.groupName, name)
	if err != nil {
		return err
	}

	return nil
}

// AttachVolume attaches a volume to an instance
func (a *Azure) AttachVolume(config *Config, image, name, mount string) error {
	vmClient := a.getVMClient()
	vm, err := vmClient.Get(context.TODO(), a.groupName, image, compute.InstanceView)
	if err != nil {
		return err
	}

	disksClient := a.getDisksClient()

	disk, err := disksClient.Get(context.TODO(), a.groupName, name)
	if err != nil {
		return err
	}

	vm.StorageProfile.DataDisks = &[]compute.DataDisk{
		{
			Lun:          to.Int32Ptr(0),
			Name:         &name,
			CreateOption: compute.DiskCreateOptionTypesAttach,
			ManagedDisk: &compute.ManagedDiskParameters{
				ID: to.StringPtr(*disk.ID),
			},
		},
	}

	future, err := vmClient.CreateOrUpdate(context.TODO(), a.groupName, image, vm)
	if err != nil {
		return fmt.Errorf("cannot update vm: %v", err)
	}

	fmt.Println("attaching the volume - this can take a few minutes - you can ctrl-c this after a bit")

	err = future.WaitForCompletionRef(context.TODO(), vmClient.Client)
	if err != nil {
		return fmt.Errorf("cannot get the vm create or update future response: %v", err)
	}

	return nil
}

// DetachVolume detachs a volume from an instance
func (a *Azure) DetachVolume(config *Config, image, name string) error {
	vmClient := a.getVMClient()
	vm, err := vmClient.Get(context.TODO(), a.groupName, image, compute.InstanceView)
	if err != nil {
		return err
	}

	dataDisks := &[]compute.DataDisk{}

	for _, disk := range *vm.StorageProfile.DataDisks {
		if *disk.Name != name {
			*dataDisks = append(*dataDisks, disk)
		}
	}

	vm.StorageProfile.DataDisks = dataDisks

	future, err := vmClient.CreateOrUpdate(context.TODO(), a.groupName, image, vm)
	if err != nil {
		return fmt.Errorf("cannot update vm: %v", err)
	}

	fmt.Println("detaching the volume - this can take a few minutes - you can ctrl-c this after a bit")

	err = future.WaitForCompletionRef(context.TODO(), vmClient.Client)
	if err != nil {
		return fmt.Errorf("cannot get the vm create or update future response: %v", err)
	}

	return nil
}

func (a *Azure) getDisksClient() compute.DisksClient {
	vmClient := compute.NewDisksClientWithBaseURI(compute.DefaultBaseURI, a.subID)
	authr, _ := a.GetResourceManagementAuthorizer()
	vmClient.Authorizer = authr
	vmClient.AddToUserAgent(userAgent)
	return vmClient
}