apiVersion: hmc.mirantis.com/v1alpha1
kind: ManagedCluster
metadata:
  name: ${MANAGED_CLUSTER_NAME}
spec:
  template: aws-hosted-cp-0-0-3
  credential: ${AWS_CLUSTER_IDENTITY}-cred
  config:
    clusterIdentity:
      name: ${AWS_CLUSTER_IDENTITY}
      namespace: ${NAMESPACE}
    vpcID: ${AWS_VPC_ID}
    region: ${AWS_REGION}
    subnets:
      - id: ${AWS_SUBNET_ID}
        availabilityZone: ${AWS_SUBNET_AVAILABILITY_ZONE}
    instanceType: ${AWS_INSTANCE_TYPE:=t3.medium}
    securityGroupIDs:
      - ${AWS_SG_ID}
