apiVersion: hmc.mirantis.com/v1alpha1
kind: ManagedCluster
metadata:
  name: ${MANAGED_CLUSTER_NAME}-eks
spec:
  config:
    clusterIdentity:
      name: aws-cluster-identity
      namespace: ${NAMESPACE}
    publicIP: ${AWS_PUBLIC_IP:=true}
    region: ${AWS_REGION}
    worker:
      instanceType: ${AWS_INSTANCE_TYPE:=t3.small}
      iamInstanceProfile: nodes.cluster-api-provider-aws.sigs.k8s.io
    workersNumber: ${WORKERS_NUMBER:=1}
    sshKeyName: ${AWS_SSH_KEY_NAME}
  template: aws-eks-0-0-1
  credential: "aws-cluster-identity-cred"
