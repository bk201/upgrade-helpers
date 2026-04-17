#!/bin/bash -eu

# Usage: harv-create-post-drain-job.sh <upgrade-name> <node-name> <type> <image> [uid]
# Example: harv-create-post-drain-job.sh hvst-upgrade-lc6xx node1 post-drain rancher/harvester-upgrade:v1.8.0-rc6

UPGRADE=$1
NODE=$2
TYPE=$3
IMAGE=$4
UPGRADE_UID=${5:-""}
  
if [ -z "$UPGRADE" ] || [ -z "$NODE" ] || [ -z "$TYPE" ] || [ -z "$IMAGE" ]; then
  echo "Usage: $0 <upgrade-name> <node-name> <type> <image> [uid]"
  echo "Example: $0 hvst-upgrade-lc6xx node1 post-drain rancher/harvester-upgrade:v1.8.0-rc6"
  exit 1
fi

# Get the UID if not provided
if [ -z "$UPGRADE_UID" ]; then
  UPGRADE_UID=$(kubectl get upgrade "$UPGRADE" -n harvester-system -o jsonpath='{.metadata.uid}' 2>/dev/null || echo "")
  if [ -z "$UPGRADE_UID" ]; then
    echo "Error: Could not fetch UID for upgrade $UPGRADE."
    exit 1
  fi
fi

SAVE_TO=/tmp/${UPGRADE}-${TYPE}-${NODE}-job.yaml
echo "Generating Job YAML and saving to $SAVE_TO"

# Generate the Job YAML
cat <<EOF >"$SAVE_TO"
apiVersion: batch/v1
kind: Job
metadata:
  finalizers:
  - wrangler.cattle.io/promote-node-controller
  labels:
    harvesterhci.io/node: $NODE
    harvesterhci.io/upgrade: $UPGRADE
    harvesterhci.io/upgradeComponent: node
    harvesterhci.io/upgradeJobType: ${TYPE}
  name: ${UPGRADE}-${TYPE}-${NODE}
  namespace: harvester-system
  ownerReferences:
  - apiVersion: harvesterhci.io/v1beta1
    kind: Upgrade
    name: $UPGRADE
    uid: $UPGRADE_UID
spec:
  backoffLimit: 6
  completionMode: NonIndexed
  manualSelector: false
  parallelism: 1
  podReplacementPolicy: TerminatingOrFailed
  suspend: false
  template:
    metadata:
      labels:
        batch.kubernetes.io/job-name: ${UPGRADE}-${TYPE}-${NODE}
        harvesterhci.io/upgrade: $UPGRADE
        harvesterhci.io/upgradeComponent: node
        harvesterhci.io/upgradeJobType: ${TYPE}
        job-name: ${UPGRADE}-${TYPE}-${NODE}
    spec:
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: kubernetes.io/hostname
                operator: In
                values:
                - $NODE
      containers:
      - args:
        - ${TYPE}
        command:
        - do_upgrade_node.sh
        env:
        - name: HARVESTER_UPGRADE_NAME
          value: $UPGRADE
        - name: HARVESTER_UPGRADE_NODE_NAME
          value: $NODE
        - name: HARVESTER_UPGRADE_POD_NAME
          valueFrom:
            fieldRef:
              apiVersion: v1
              fieldPath: metadata.name
        image: ${IMAGE}
        imagePullPolicy: IfNotPresent
        name: apply
        resources: {}
        securityContext:
          capabilities:
            add:
            - CAP_SYS_BOOT
          privileged: true
        terminationMessagePath: /dev/termination-log
        terminationMessagePolicy: File
        volumeMounts:
        - mountPath: /host
          name: host-root
      dnsPolicy: ClusterFirstWithHostNet
      hostIPC: true
      hostNetwork: true
      hostPID: true
      restartPolicy: Never
      schedulerName: default-scheduler
      securityContext: {}
      serviceAccount: harvester
      serviceAccountName: harvester
      terminationGracePeriodSeconds: 30
      tolerations:
      - effect: NoSchedule
        key: node.kubernetes.io/unschedulable
        operator: Exists
      - effect: NoExecute
        key: node-role.kubernetes.io/control-plane
        operator: Exists
      - effect: NoExecute
        key: node-role.kubernetes.io/etcd
        operator: Exists
      - effect: NoSchedule
        key: kubevirt.io/drain
        operator: Exists
      - key: CriticalAddonsOnly
        operator: Exists
      - effect: NoExecute
        key: node.kubernetes.io/unreachable
        operator: Exists
      - effect: NoSchedule
        key: kubernetes.io/arch
        operator: Equal
        value: amd64
      - effect: NoSchedule
        key: kubernetes.io/arch
        operator: Equal
        value: arm64
      - effect: NoSchedule
        key: kubernetes.io/arch
        operator: Equal
        value: arm
      volumes:
      - hostPath:
          path: /
          type: Directory
        name: host-root
  ttlSecondsAfterFinished: 604800
EOF



