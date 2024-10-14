apiVersion: hmc.mirantis.com/v1alpha1
kind: ManagedCluster
metadata:
  name: ${MANAGED_CLUSTER_NAME}
spec:
  template: vsphere-standalone-cp-0-0-2
  credential: ${VSPHERE_CLUSTER_IDENTITY}-cred
  config:
    controlPlaneNumber: ${CONTROL_PLANE_NUMBER:=1}
    workersNumber: ${WORKERS_NUMBER:=1}
    clusterIdentity:
      name: "${VSPHERE_CLUSTER_IDENTITY}"
    vsphere:
      server: "${VSPHERE_SERVER}"
      thumbprint: "${VSPHERE_THUMBPRINT} "
      datacenter: "${VSPHERE_DATACENTER}"
      datastore: "${VSPHERE_DATASTORE}"
      resourcePool: "${VSPHERE_RESOURCEPOOL}"
      folder: "${VSPHERE_FOLDER}"
      username: "${VSPHERE_USER}"
      password: "${VSPHERE_PASSWORD}"
    controlPlaneEndpointIP: "${VSPHERE_CONTROL_PLANE_ENDPOINT}"

    controlPlane:
      ssh:
        user: ubuntu
        publicKey: "${VSPHERE_SSH_KEY}"
      rootVolumeSize: 50
      cpus: 4
      memory: 4096
      vmTemplate: "${VSPHERE_VM_TEMPLATE}"
      network: "${VSPHERE_NETWORK}"

    worker:
      ssh:
        user: ubuntu
        publicKey: "${VSPHERE_SSH_KEY}"
      rootVolumeSize: 50
      cpus: 4
      memory: 4096
      vmTemplate: "${VSPHERE_VM_TEMPLATE}"
      network: "${VSPHERE_NETWORK}"