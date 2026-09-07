package svcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const (
	kafkaDataVolume = "data"
	kafkaDataMount  = "/var/lib/kafka/data"
	kafkaSecretDir  = "/run/opsi-kafka"
)

func kafkaClusterID(resourceID string) string {
	sum := sha256.Sum256([]byte("opsi-kafka-cluster-" + resourceID))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

func kafkaManagedResourceObjects(spec resourcev1.ManagedResourceSpec, credential *resourcev1.ManagedResourceCredential) []map[string]any {
	if credential == nil || credential.ValidateFor(resourcev1.TypeKafka) != nil || credential.CredentialID != spec.CredentialID {
		return nil
	}
	namespace := managedResourceNamespace(spec)
	labels := managedResourceLabels(spec)
	selector := managedResourceOwnershipLabels(spec)
	annotations := managedResourceAnnotations(spec)
	secretName := managedResourceSecretName(spec)
	pvcName := managedResourcePVCName(spec)

	jaasConfig := fmt.Sprintf("KafkaServer {\n    org.apache.kafka.common.security.plain.PlainLoginModule required\n    username=%q\n    password=%q\n    user_%s=%q;\n};\n", credential.Username, credential.Password, credential.Username, credential.Password)
	clientProperties := fmt.Sprintf("security.protocol=SASL_PLAINTEXT\nsasl.mechanism=PLAIN\nsasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username=%q password=%q;\n", credential.Username, credential.Password)

	secret := map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]any{"name": secretName, "namespace": namespace, "labels": labels, "annotations": annotations},
		"data": map[string]any{
			"username":               base64.StdEncoding.EncodeToString([]byte(credential.Username)),
			"password":               base64.StdEncoding.EncodeToString([]byte(credential.Password)),
			"kafka_server_jaas.conf": base64.StdEncoding.EncodeToString([]byte(jaasConfig)),
			"client.properties":      base64.StdEncoding.EncodeToString([]byte(clientProperties)),
		},
	}

	pvc := map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": pvcName, "namespace": namespace, "labels": managedResourceOwnershipLabels(spec), "annotations": managedPVCAnnotations(spec)},
		"spec": map[string]any{
			"accessModes": []any{"ReadWriteOnce"},
			"resources":   map[string]any{"requests": map[string]any{"storage": strconv.FormatInt(spec.Storage.SizeBytes, 10)}},
		},
	}

	numPartitions := "3"
	if v, ok := spec.ServiceConfig["num_partitions"]; ok && v != "" {
		numPartitions = v
	}
	retentionHours := "168"
	if v, ok := spec.ServiceConfig["retention_hours"]; ok && v != "" {
		retentionHours = v
	}

	container := map[string]any{
		"name": "kafka", "image": spec.Image, "imagePullPolicy": "IfNotPresent",
		"ports": []any{map[string]any{"name": "kafka", "containerPort": int32(9092), "protocol": "TCP"}},
		"env": []any{
			map[string]any{"name": "CLUSTER_ID", "value": kafkaClusterID(spec.ResourceID)},
			map[string]any{"name": "KAFKA_NODE_ID", "value": "1"},
			map[string]any{"name": "KAFKA_PROCESS_ROLES", "value": "broker,controller"},
			map[string]any{"name": "KAFKA_CONTROLLER_QUORUM_VOTERS", "value": "1@127.0.0.1:9093"},
			map[string]any{"name": "KAFKA_LISTENERS", "value": "SASL_PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093"},
			map[string]any{"name": "KAFKA_ADVERTISED_LISTENERS", "value": fmt.Sprintf("SASL_PLAINTEXT://%s:9092", spec.Connection.Host)},
			map[string]any{"name": "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP", "value": "SASL_PLAINTEXT:SASL_PLAINTEXT,CONTROLLER:PLAINTEXT"},
			map[string]any{"name": "KAFKA_CONTROLLER_LISTENER_NAMES", "value": "CONTROLLER"},
			map[string]any{"name": "KAFKA_SASL_ENABLED_MECHANISMS", "value": "PLAIN"},
			map[string]any{"name": "KAFKA_SASL_MECHANISM_INTER_BROKER_PROTOCOL", "value": "PLAIN"},
			map[string]any{"name": "KAFKA_INTER_BROKER_LISTENER_NAME", "value": "SASL_PLAINTEXT"},
			map[string]any{"name": "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR", "value": "1"},
			map[string]any{"name": "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR", "value": "1"},
			map[string]any{"name": "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR", "value": "1"},
			map[string]any{"name": "KAFKA_LOG_DIRS", "value": kafkaDataMount},
			map[string]any{"name": "KAFKA_AUTO_CREATE_TOPICS_ENABLE", "value": "false"},
			map[string]any{"name": "KAFKA_NUM_PARTITIONS", "value": numPartitions},
			map[string]any{"name": "KAFKA_LOG_RETENTION_HOURS", "value": retentionHours},
			map[string]any{"name": "KAFKA_HEAP_OPTS", "value": "-Xms512m -Xmx512m"},
			map[string]any{"name": "KAFKA_OPTS", "value": "-Djava.security.auth.login.config=" + kafkaSecretDir + "/kafka_server_jaas.conf"},
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": fmt.Sprintf("%dm", spec.CPUMillicores), "memory": strconv.FormatInt(spec.MemoryBytes, 10)},
			"limits":   map[string]any{"cpu": fmt.Sprintf("%dm", spec.CPUMillicores), "memory": strconv.FormatInt(spec.MemoryBytes, 10)},
		},
		"volumeMounts": []any{
			map[string]any{"name": kafkaDataVolume, "mountPath": kafkaDataMount},
			map[string]any{"name": "server-credential", "mountPath": kafkaSecretDir, "readOnly": true},
		},
		"readinessProbe": map[string]any{
			"tcpSocket":           map[string]any{"port": "kafka"},
			"initialDelaySeconds": 3,
			"periodSeconds":       2,
			"timeoutSeconds":      1,
			"failureThreshold":    30,
		},
	}

	podSpec := map[string]any{
		"securityContext": map[string]any{"fsGroup": int64(1000), "runAsUser": int64(1000)},
		"containers":      []any{container},
		"volumes": []any{
			map[string]any{"name": kafkaDataVolume, "persistentVolumeClaim": map[string]any{"claimName": pvcName}},
			map[string]any{"name": "server-credential", "secret": map[string]any{"secretName": secretName, "defaultMode": 256}},
		},
	}

	statefulSet := map[string]any{
		"apiVersion": "apps/v1", "kind": "StatefulSet",
		"metadata": map[string]any{"name": spec.Connection.ServiceName, "namespace": namespace, "labels": labels, "annotations": annotations},
		"spec": map[string]any{
			"serviceName": spec.Connection.ServiceName, "replicas": int32(1),
			"selector":       map[string]any{"matchLabels": selector},
			"updateStrategy": map[string]any{"type": "RollingUpdate"},
			"template":       map[string]any{"metadata": map[string]any{"labels": labels, "annotations": annotations}, "spec": podSpec},
		},
	}

	service := map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": spec.Connection.ServiceName, "namespace": namespace, "labels": labels, "annotations": annotations},
		"spec":     map[string]any{"type": "ClusterIP", "selector": selector, "ports": []any{map[string]any{"name": "kafka", "port": int32(9092), "targetPort": "kafka", "protocol": "TCP"}}},
	}

	return []map[string]any{secret, pvc, statefulSet, service}
}

func kafkaTopicsJobName(spec resourcev1.ManagedResourceSpec) string {
	hash := spec.ConfigurationHash
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return deploymentv1.StableDNSName("omr", spec.ResourceID, "topics", hash)
}

func kafkaTopicsJob(spec resourcev1.ManagedResourceSpec) map[string]any {
	labels := managedResourceLabels(spec)
	labels["opsi.dev/kafka-topics"] = "true"
	args := make([]string, 0, len(spec.Topics))
	for _, topic := range spec.Topics {
		command := fmt.Sprintf("/opt/kafka/bin/kafka-topics.sh --bootstrap-server \"$1\" --command-config %s/client.properties --create --if-not-exists --topic %s --partitions %d --replication-factor 1", kafkaSecretDir, topic.Name, topic.Partitions)
		if topic.RetentionHours > 0 {
			command += fmt.Sprintf(" --config retention.ms=%d", int64(topic.RetentionHours)*3600*1000)
		}
		args = append(args, command)
	}
	script := strings.Join(args, "\n")
	namespace, secretName := managedResourceNamespace(spec), managedResourceSecretName(spec)
	return map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": kafkaTopicsJobName(spec), "namespace": namespace, "labels": labels, "annotations": managedResourceAnnotations(spec)},
		"spec": map[string]any{
			"backoffLimit": int32(3), "ttlSecondsAfterFinished": int32(3600),
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec": map[string]any{
					// The client properties are deliberately mounted mode 0400. Keep
					// the topic Job in the same explicit non-root security context as
					// the broker, otherwise the image's app user cannot read them.
					"securityContext": map[string]any{"fsGroup": int64(1000), "runAsUser": int64(1000)},
					"restartPolicy":   "Never",
					"containers": []any{map[string]any{
						"name": "kafka-topics", "image": spec.Image, "imagePullPolicy": "IfNotPresent",
						"command":      []any{"sh", "-ec", script, "opsi-kafka-topics", spec.Connection.Host + ":9092"},
						"volumeMounts": []any{map[string]any{"name": "client-credential", "mountPath": kafkaSecretDir, "readOnly": true}},
					}},
					"volumes": []any{map[string]any{"name": "client-credential", "secret": map[string]any{"secretName": secretName, "defaultMode": 256}}},
				},
			},
		},
	}
}

func (r ManagedResourceReconciler) ensureKafkaTopics(ctx context.Context, spec resourcev1.ManagedResourceSpec, evidence *resourcev1.ManagedResourceEvidence) (*resourcev1.ManagedResourceEvidence, error) {
	if len(spec.Topics) == 0 {
		evidence.TopicsReady = true
		return evidence, nil
	}
	job := kafkaTopicsJob(spec)
	current, err := r.get(ctx, "job", kafkaTopicsJobName(spec), managedResourceNamespace(spec))
	if err != nil {
		return evidence, err
	}
	if current != nil && !exactManagedResourceOwnership(current, spec) {
		return evidence, managedResourceError{resourcev1.FailureApplyFailed, "managed Kafka topic Job has different ownership"}
	}
	if current != nil && number(nested(current, "status", "failed")) > 0 {
		// Jobs are immutable. A terminal Job from a prior failed reconciliation
		// must be removed before the same authoritative spec can be retried.
		if _, err := r.run(ctx, nil, "delete", "job", kafkaTopicsJobName(spec), "-n", managedResourceNamespace(spec), "--ignore-not-found", "--wait=true", "--timeout=2m"); err != nil {
			return evidence, managedResourceError{resourcev1.FailureApplyFailed, "managed Kafka failed topic Job cleanup failed"}
		}
		current = nil
	}
	if current == nil {
		data, _ := json.Marshal(job)
		if _, err := r.run(ctx, data, "create", "--field-manager="+managedResourceFieldManager, "-f", "-"); err != nil {
			return evidence, managedResourceError{resourcev1.FailureApplyFailed, "managed Kafka topic Job apply failed"}
		}
	}
	deadline := time.NewTimer(r.kafkaTopicTimeout())
	defer deadline.Stop()
	ticker := time.NewTicker(r.kafkaTopicPollInterval())
	defer ticker.Stop()
	for {
		current, err = r.get(ctx, "job", kafkaTopicsJobName(spec), managedResourceNamespace(spec))
		if err != nil {
			return evidence, err
		}
		if current != nil && number(nested(current, "status", "succeeded")) >= 1 {
			evidence.TopicsReady = true
			return evidence, nil
		}
		if current != nil && number(nested(current, "status", "failed")) > 0 {
			return evidence, managedResourceError{resourcev1.FailureReadinessFailed, "managed Kafka topic Job failed"}
		}
		select {
		case <-ctx.Done():
			return evidence, ctx.Err()
		case <-deadline.C:
			return evidence, managedResourceError{resourcev1.FailureReadinessFailed, "managed Kafka topic Job timed out"}
		case <-ticker.C:
		}
	}
}

func (r ManagedResourceReconciler) kafkaTopicTimeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 3 * time.Minute
}

func (r ManagedResourceReconciler) kafkaTopicPollInterval() time.Duration {
	if r.PollInterval > 0 {
		return r.PollInterval
	}
	return 2 * time.Second
}

func (r ManagedResourceReconciler) observeKafka(ctx context.Context, spec resourcev1.ManagedResourceSpec) (*resourcev1.ManagedResourceEvidence, error) {
	namespace := managedResourceNamespace(spec)
	statefulSet, err := r.get(ctx, "statefulset", spec.Connection.ServiceName, namespace)
	if err != nil || statefulSet == nil || !exactManagedResourceOwnership(statefulSet, spec) {
		return &resourcev1.ManagedResourceEvidence{}, err
	}
	service, err := r.get(ctx, "service", spec.Connection.ServiceName, namespace)
	if err != nil || service == nil || !exactManagedResourceOwnership(service, spec) {
		return &resourcev1.ManagedResourceEvidence{}, err
	}
	secret, err := r.get(ctx, "secret", managedResourceSecretName(spec), namespace)
	if err != nil || secret == nil || !exactManagedResourceOwnership(secret, spec) {
		if err != nil {
			return &resourcev1.ManagedResourceEvidence{}, err
		}
		return &resourcev1.ManagedResourceEvidence{}, managedResourceError{resourcev1.FailureSecretApplyFailed, "managed Kafka server credential Secret is unavailable"}
	}
	pvc, err := r.get(ctx, "persistentvolumeclaim", managedResourcePVCName(spec), namespace)
	if err != nil || pvc == nil || !exactManagedResourceOwnership(pvc, spec) || !managedPVCMatchesIntent(pvc, spec) {
		return &resourcev1.ManagedResourceEvidence{}, err
	}
	podsRaw, err := r.run(ctx, nil, "get", "pods", "-n", namespace, "-l", selectorString(managedResourceLabels(spec)), "-o", "json")
	if err != nil {
		return nil, err
	}
	var pods map[string]any
	if jsonErr := json.Unmarshal(podsRaw, &pods); jsonErr != nil {
		return nil, managedResourceError{resourcev1.FailureReadinessFailed, "invalid Kubernetes pod evidence"}
	}
	readyReplicas := int32(number(nested(statefulSet, "status", "readyReplicas")))
	image, imageID, podReady := managedPodEvidence(pods, spec)
	currentRevision, _ := nested(statefulSet, "status", "currentRevision").(string)
	updateRevision, _ := nested(statefulSet, "status", "updateRevision").(string)
	workloadReady := readyReplicas >= spec.Replicas && number(nested(statefulSet, "status", "observedGeneration")) >= number(nested(statefulSet, "metadata", "generation")) && (currentRevision == "" || currentRevision == updateRevision)
	clusterIP, _ := nested(service, "spec", "clusterIP").(string)
	serviceReady := clusterIP != "" && clusterIP != "None" && serviceHasPort(service, 9092)
	pvName, _ := nested(pvc, "spec", "volumeName").(string)
	storageClass, _ := nested(pvc, "spec", "storageClassName").(string)
	actualStorage, _ := nested(pvc, "status", "capacity", "storage").(string)
	storageReady := nested(pvc, "status", "phase") == "Bound" && pvName != ""
	volumeMounted := kafkaVolumeMounted(pods, spec)

	evidence := &resourcev1.ManagedResourceEvidence{
		ObservedSpecHash: spec.SpecHash, WorkloadReady: workloadReady, PodReady: podReady >= spec.Replicas, ServiceReady: serviceReady,
		SecretReady: true, Image: image, ImageID: imageID, AvailableReplicas: readyReplicas,
		StorageReady: storageReady, VolumeMounted: volumeMounted, PVCName: managedResourcePVCName(spec), PVName: pvName,
		StorageClass: storageClass, RequestedBytes: spec.Storage.SizeBytes, ActualStorage: actualStorage, ObservedAt: time.Now().UTC(),
	}

	if workloadReady && evidence.PodReady && serviceReady && storageReady && volumeMounted {
		probe := `/opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server "$1" --command-config /run/opsi-kafka/client.properties >/dev/null`
		if _, authErr := r.run(ctx, nil, "exec", "pod/"+spec.Connection.ServiceName+"-0", "-n", namespace, "-c", "kafka", "--", "sh", "-ec", probe, "opsi-kafka-readiness", spec.Connection.Host+":9092"); authErr != nil {
			return evidence, managedResourceError{resourcev1.FailureAuthFailed, "managed Kafka authenticated readiness check failed"}
		}
		evidence.AuthReady = true
	}
	return evidence, nil
}

func kafkaVolumeMounted(pods map[string]any, spec resourcev1.ManagedResourceSpec) bool {
	items, _ := pods["items"].([]any)
	for _, raw := range items {
		pod, _ := raw.(map[string]any)
		volumes, _ := nested(pod, "spec", "volumes").([]any)
		claimFound := false
		for _, rawVolume := range volumes {
			volume, _ := rawVolume.(map[string]any)
			if volume["name"] == kafkaDataVolume && nested(volume, "persistentVolumeClaim", "claimName") == managedResourcePVCName(spec) {
				claimFound = true
			}
		}
		containers, _ := nested(pod, "spec", "containers").([]any)
		for _, rawContainer := range containers {
			container, _ := rawContainer.(map[string]any)
			if container["name"] != "kafka" {
				continue
			}
			mounts, _ := container["volumeMounts"].([]any)
			for _, rawMount := range mounts {
				mount, _ := rawMount.(map[string]any)
				if claimFound && mount["name"] == kafkaDataVolume && mount["mountPath"] == kafkaDataMount {
					return true
				}
			}
		}
	}
	return false
}
