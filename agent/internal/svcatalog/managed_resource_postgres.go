package svcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const (
	postgresDataVolume = "data"
	postgresDataMount  = "/var/lib/postgresql"
	postgresDataDir    = "/var/lib/postgresql/18/docker"
	postgresSecretDir  = "/run/opsi-postgres"
)

func managedCredentialRequired(resourceType resourcev1.Type) bool {
	return resourceType == resourcev1.TypeRedis || resourceType == resourcev1.TypePostgres
}

func managedResourcePVCName(spec resourcev1.ManagedResourceSpec) string {
	return deploymentv1.StableDNSName(spec.Connection.ServiceName, "data")
}

func postgresManagedResourceObjects(spec resourcev1.ManagedResourceSpec, credential *resourcev1.ManagedResourceCredential) []map[string]any {
	if credential == nil || credential.ValidateFor(resourcev1.TypePostgres) != nil || credential.CredentialID != spec.CredentialID {
		return nil
	}
	namespace := managedResourceNamespace(spec)
	labels := managedResourceLabels(spec)
	selector := managedResourceOwnershipLabels(spec)
	annotations := managedResourceAnnotations(spec)
	secretName := managedResourceSecretName(spec)
	pvcName := managedResourcePVCName(spec)
	secret := map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]any{"name": secretName, "namespace": namespace, "labels": labels, "annotations": annotations},
		"data": map[string]any{
			"username": base64.StdEncoding.EncodeToString([]byte(credential.Username)),
			"password": base64.StdEncoding.EncodeToString([]byte(credential.Password)),
			"database": base64.StdEncoding.EncodeToString([]byte(credential.Database)),
		},
	}
	pvc := map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": pvcName, "namespace": namespace, "labels": managedResourceOwnershipLabels(spec), "annotations": postgresPVCAnnotations(spec)},
		"spec": map[string]any{
			"accessModes": []any{"ReadWriteOnce"},
			"resources":   map[string]any{"requests": map[string]any{"storage": strconv.FormatInt(spec.Storage.SizeBytes, 10)}},
		},
	}
	readiness := `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); pg_isready -q -h 127.0.0.1 -U "$u" -d "$d" && test "$(psql -h 127.0.0.1 -U "$u" -d "$d" -tAc 'SELECT 1')" = 1`
	container := map[string]any{
		"name": "postgres", "image": spec.Image, "imagePullPolicy": "IfNotPresent",
		"ports": []any{map[string]any{"name": "postgres", "containerPort": int32(5432), "protocol": "TCP"}},
		"env": []any{
			map[string]any{"name": "POSTGRES_USER_FILE", "value": postgresSecretDir + "/username"},
			map[string]any{"name": "POSTGRES_PASSWORD_FILE", "value": postgresSecretDir + "/password"},
			map[string]any{"name": "POSTGRES_DB_FILE", "value": postgresSecretDir + "/database"},
			map[string]any{"name": "PGDATA", "value": postgresDataDir},
			map[string]any{"name": "POSTGRES_INITDB_ARGS", "value": "--auth-host=scram-sha-256"},
		},
		"resources": map[string]any{"requests": map[string]any{"cpu": fmt.Sprintf("%dm", spec.CPUMillicores), "memory": strconv.FormatInt(spec.MemoryBytes, 10)}, "limits": map[string]any{"cpu": fmt.Sprintf("%dm", spec.CPUMillicores), "memory": strconv.FormatInt(spec.MemoryBytes, 10)}},
		"volumeMounts": []any{
			map[string]any{"name": postgresDataVolume, "mountPath": postgresDataMount},
			map[string]any{"name": "server-credential", "mountPath": postgresSecretDir, "readOnly": true},
		},
		"readinessProbe": map[string]any{"exec": map[string]any{"command": []any{"sh", "-ec", readiness}}, "initialDelaySeconds": 2, "periodSeconds": 2, "timeoutSeconds": 3, "failureThreshold": 30},
	}
	podSpec := map[string]any{
		"containers": []any{container},
		"volumes": []any{
			map[string]any{"name": postgresDataVolume, "persistentVolumeClaim": map[string]any{"claimName": pvcName}},
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
		"spec":     map[string]any{"type": "ClusterIP", "selector": selector, "ports": []any{map[string]any{"name": "postgres", "port": int32(5432), "targetPort": "postgres", "protocol": "TCP"}}},
	}
	return []map[string]any{secret, pvc, statefulSet, service}
}

func postgresPVCAnnotations(spec resourcev1.ManagedResourceSpec) map[string]string {
	annotations := managedResourceOwnershipAnnotations(spec)
	annotations["opsi.dev/storage-policy"] = spec.Storage.PolicyRef
	annotations["opsi.dev/storage-size-bytes"] = strconv.FormatInt(spec.Storage.SizeBytes, 10)
	annotations["opsi.dev/storage-hash"] = postgresStorageHash(spec)
	return annotations
}

func postgresStorageHash(spec resourcev1.ManagedResourceSpec) string {
	sum := sha256.Sum256([]byte(spec.ResourceID + "\x00" + spec.Storage.PolicyRef + "\x00" + strconv.FormatInt(spec.Storage.SizeBytes, 10)))
	return hex.EncodeToString(sum[:])
}

func postgresPVCMatchesIntent(pvc map[string]any, spec resourcev1.ManagedResourceSpec) bool {
	annotations := stringMap(nested(pvc, "metadata", "annotations"))
	return metadataString(pvc, "name") == managedResourcePVCName(spec) &&
		annotations["opsi.dev/storage-policy"] == spec.Storage.PolicyRef &&
		annotations["opsi.dev/storage-size-bytes"] == strconv.FormatInt(spec.Storage.SizeBytes, 10) &&
		annotations["opsi.dev/storage-hash"] == postgresStorageHash(spec)
}

func (r ManagedResourceReconciler) observePostgres(ctx context.Context, spec resourcev1.ManagedResourceSpec) (*resourcev1.ManagedResourceEvidence, error) {
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
		return &resourcev1.ManagedResourceEvidence{}, managedResourceError{resourcev1.FailureSecretApplyFailed, "managed PostgreSQL server credential Secret is unavailable"}
	}
	pvc, err := r.get(ctx, "persistentvolumeclaim", managedResourcePVCName(spec), namespace)
	if err != nil || pvc == nil || !exactManagedResourceOwnership(pvc, spec) || !postgresPVCMatchesIntent(pvc, spec) {
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
	serviceReady := clusterIP != "" && clusterIP != "None" && serviceHasPort(service, 5432)
	pvName, _ := nested(pvc, "spec", "volumeName").(string)
	storageClass, _ := nested(pvc, "spec", "storageClassName").(string)
	actualStorage, _ := nested(pvc, "status", "capacity", "storage").(string)
	storageReady := nested(pvc, "status", "phase") == "Bound" && pvName != ""
	volumeMounted := postgresVolumeMounted(pods, spec)
	evidence := &resourcev1.ManagedResourceEvidence{
		ObservedSpecHash: spec.SpecHash, WorkloadReady: workloadReady, PodReady: podReady >= spec.Replicas, ServiceReady: serviceReady,
		SecretReady: true, Image: image, ImageID: imageID, AvailableReplicas: readyReplicas,
		StorageReady: storageReady, VolumeMounted: volumeMounted, PVCName: managedResourcePVCName(spec), PVName: pvName,
		StorageClass: storageClass, RequestedBytes: spec.Storage.SizeBytes, ActualStorage: actualStorage, ObservedAt: time.Now().UTC(),
	}
	if workloadReady && evidence.PodReady && serviceReady && storageReady && volumeMounted {
		probe := `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); pg_isready -q -h "$1" -U "$u" -d "$d" && test "$(psql -h "$1" -U "$u" -d "$d" -tAc 'SELECT 1')" = 1`
		if _, authErr := r.run(ctx, nil, "exec", "pod/"+spec.Connection.ServiceName+"-0", "-n", namespace, "-c", "postgres", "--", "sh", "-ec", probe, "opsi-readiness", spec.Connection.Host); authErr != nil {
			return evidence, managedResourceError{resourcev1.FailureAuthFailed, "managed PostgreSQL authenticated readiness check failed"}
		}
		evidence.AuthReady = true
	}
	return evidence, nil
}

func postgresVolumeMounted(pods map[string]any, spec resourcev1.ManagedResourceSpec) bool {
	items, _ := pods["items"].([]any)
	for _, raw := range items {
		pod, _ := raw.(map[string]any)
		volumes, _ := nested(pod, "spec", "volumes").([]any)
		claimFound := false
		for _, rawVolume := range volumes {
			volume, _ := rawVolume.(map[string]any)
			if volume["name"] == postgresDataVolume && nested(volume, "persistentVolumeClaim", "claimName") == managedResourcePVCName(spec) {
				claimFound = true
			}
		}
		containers, _ := nested(pod, "spec", "containers").([]any)
		for _, rawContainer := range containers {
			container, _ := rawContainer.(map[string]any)
			if container["name"] != "postgres" {
				continue
			}
			mounts, _ := container["volumeMounts"].([]any)
			for _, rawMount := range mounts {
				mount, _ := rawMount.(map[string]any)
				if claimFound && mount["name"] == postgresDataVolume && mount["mountPath"] == postgresDataMount {
					return true
				}
			}
		}
	}
	return false
}
