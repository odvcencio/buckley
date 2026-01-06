package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	workspaceLabelKey   = "buckley.io/workspace"
	workspaceLabelValue = "task"
)

type BatchCoordinator struct {
	cfg       config.BatchConfig
	workflow  *WorkflowManager
	client    kubernetes.Interface
	namespace string
}

type BatchTaskResult struct {
	JobName      string
	RemoteBranch string
}

func NewBatchCoordinator(cfg config.BatchConfig, workflow *WorkflowManager) (*BatchCoordinator, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	client, err := buildKubeClient(cfg.Kubeconfig)
	if err != nil {
		return nil, err
	}
	namespace := detectNamespace(cfg.Namespace)
	return &BatchCoordinator{cfg: cfg, workflow: workflow, client: client, namespace: namespace}, nil
}

func (b *BatchCoordinator) Enabled() bool {
	return b != nil && b.client != nil && b.cfg.Enabled
}

func (b *BatchCoordinator) DispatchTask(ctx context.Context, plan *Plan, task *Task) (*BatchTaskResult, error) {
	if !b.Enabled() {
		return nil, fmt.Errorf("batch coordinator is not enabled")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	remoteBranch := b.remoteBranchForTask(plan, task)
	vars := b.templateVars(plan, task, remoteBranch)
	jobName := b.buildJobName(plan, task)

	job := b.buildJob(jobName, vars)

	// Ensure idempotency by deleting any prior job with the same name
	propagation := metav1.DeletePropagationBackground
	_ = b.client.BatchV1().Jobs(b.namespace).Delete(context.Background(), jobName, metav1.DeleteOptions{PropagationPolicy: &propagation})

	created, err := b.client.BatchV1().Jobs(b.namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		recordJobFailure()
		return nil, fmt.Errorf("creating batch job: %w", err)
	}
	recordJobDispatch()

	if b.workflow != nil {
		msg := fmt.Sprintf("🚀 Started batch job %s for task %s", created.Name, task.Title)
		if remoteBranch != "" {
			msg = fmt.Sprintf("%s → remote branch %s", msg, remoteBranch)
		}
		b.workflow.SendProgress(msg)
	}

	if b.cfg.WaitForCompletion {
		if err := b.waitForCompletion(ctx, jobName); err != nil {
			recordJobFailure()
			return nil, err
		}
		if b.cfg.FollowLogs {
			b.emitJobLogs(ctx, jobName)
		}
	}

	return &BatchTaskResult{JobName: jobName, RemoteBranch: remoteBranch}, nil
}

func (b *BatchCoordinator) waitForCompletion(ctx context.Context, jobName string) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		job, err := b.client.BatchV1().Jobs(b.namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("batch job %s disappeared before completion", jobName)
			}
			return err
		}
		if job.Status.Succeeded > 0 {
			return nil
		}
		if job.Status.Failed > 0 && job.Spec.BackoffLimit != nil && job.Status.Failed >= *job.Spec.BackoffLimit {
			return fmt.Errorf("batch job %s failed after %d attempts", jobName, job.Status.Failed)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *BatchCoordinator) emitJobLogs(ctx context.Context, jobName string) {
	logs, err := b.collectJobLogs(ctx, jobName)
	if err != nil {
		if b.workflow != nil {
			b.workflow.SendProgress(fmt.Sprintf("⚠️ Unable to read logs for job %s: %v", jobName, err))
		}
		return
	}
	if strings.TrimSpace(logs) == "" {
		return
	}
	if b.workflow != nil {
		b.workflow.SendProgress(fmt.Sprintf("📄 Batch job %s log tail:\n%s", jobName, logs))
	}
}

func (b *BatchCoordinator) collectJobLogs(ctx context.Context, jobName string) (string, error) {
	pods, err := b.client.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("job-name=%s", jobName)})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods for job %s", jobName)
	}
	pod := pods.Items[0]
	limit := int64(4000)
	req := b.client.CoreV1().Pods(b.namespace).GetLogs(pod.Name, &corev1.PodLogOptions{TailLines: &limit})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (b *BatchCoordinator) CleanupWorkspaces(ctx context.Context, olderThan time.Duration) (int, error) {
	if !b.Enabled() {
		return 0, fmt.Errorf("batch coordinator is not enabled")
	}
	if olderThan <= 0 {
		olderThan = 4 * time.Hour
	}
	selector := fmt.Sprintf("%s=%s", workspaceLabelKey, workspaceLabelValue)
	pvcs, err := b.client.CoreV1().PersistentVolumeClaims(b.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	deleted := 0
	for _, pvc := range pvcs.Items {
		if pvc.CreationTimestamp.IsZero() || pvc.CreationTimestamp.Time.After(cutoff) {
			continue
		}
		if len(pvc.OwnerReferences) > 0 {
			continue
		}
		if err := b.client.CoreV1().PersistentVolumeClaims(b.namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return deleted, err
		}
		deleted++
	}
	recordWorkspacePrune(deleted)
	return deleted, nil
}

func (b *BatchCoordinator) buildJob(jobName string, vars map[string]string) *batchv1.Job {
	labels := map[string]string{
		"app":                    "buckley",
		"app.kubernetes.io/name": "buckley",
		"buckley.plan":           truncateIdentifier(vars["PLAN_SLUG"], 30),
		"buckley.task":           truncateIdentifier(vars["TASK_SLUG"], 30),
	}
	workspaceLabels := map[string]string{
		workspaceLabelKey: workspaceLabelValue,
		"buckley.plan":    labels["buckley.plan"],
		"buckley.task":    labels["buckley.task"],
	}

	template := b.cfg.JobTemplate
	workspaceConfigured := strings.TrimSpace(template.WorkspaceClaim) != "" || template.WorkspaceVolumeTemplate != nil
	workspaceMountPath := strings.TrimSpace(template.WorkspaceMountPath)
	if workspaceMountPath == "" {
		workspaceMountPath = "/workspace"
	}
	vars["WORKSPACE_DIR"] = workspaceMountPath

	command := b.applyTemplates(template.Command, vars)
	args := b.applyTemplates(template.Args, vars)

	envVars := []corev1.EnvVar{}
	setEnvVar(&envVars, "BUCKLEY_PLAN_ID", vars["PLAN_ID"])
	setEnvVar(&envVars, "BUCKLEY_TASK_ID", vars["TASK_ID"])
	setEnvVar(&envVars, "BUCKLEY_TASK_TITLE", vars["TASK_TITLE"])
	setEnvVar(&envVars, "BUCKLEY_TASK_TYPE", vars["TASK_TYPE"])
	setEnvVar(&envVars, "BUCKLEY_FEATURE_NAME", vars["FEATURE"])
	setEnvVar(&envVars, "BUCKLEY_GIT_BRANCH", vars["GIT_BRANCH"])
	if remote := vars["REMOTE_BRANCH"]; remote != "" {
		setEnvVar(&envVars, "BUCKLEY_REMOTE_BRANCH", remote)
	}
	if remoteName := vars["REMOTE_NAME"]; remoteName != "" {
		setEnvVar(&envVars, "BUCKLEY_REMOTE_NAME", remoteName)
	}

	setEnvVar(&envVars, "BUCKLEY_BATCH_ENABLED", "0")
	if workspaceConfigured {
		setEnvVarIfMissing(&envVars, "BUCKLEY_TASK_WORKDIR", workspaceMountPath)
	}
	if repoURL := strings.TrimSpace(vars["REPO_URL"]); repoURL != "" {
		setEnvVarIfMissing(&envVars, "BUCKLEY_PLAN_REPO_URL", repoURL)
	}

	for key, value := range template.Env {
		setEnvVar(&envVars, key, b.applyTemplate(value, vars))
	}

	var envFrom []corev1.EnvFromSource
	for _, name := range template.EnvFromSecrets {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
			},
		})
	}
	for _, name := range template.EnvFromConfigMaps {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		envFrom = append(envFrom, corev1.EnvFromSource{
			ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
			},
		})
	}

	container := corev1.Container{
		Name:            "buckley-task",
		Image:           template.Image,
		ImagePullPolicy: corev1.PullPolicy(template.ImagePullPolicy),
		Command:         command,
		Args:            args,
		Env:             envVars,
	}
	if workspaceConfigured {
		container.WorkingDir = workspaceMountPath
	}
	if len(template.Resources.Limits) > 0 || len(template.Resources.Requests) > 0 {
		container.Resources = template.Resources
	}
	if len(envFrom) > 0 {
		container.EnvFrom = envFrom
	}

	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	if claim := strings.TrimSpace(template.WorkspaceClaim); claim != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: workspaceMountPath})
	} else if tpl := template.WorkspaceVolumeTemplate; tpl != nil {
		modes := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		if len(tpl.AccessModes) > 0 {
			modes = make([]corev1.PersistentVolumeAccessMode, 0, len(tpl.AccessModes))
			for _, mode := range tpl.AccessModes {
				switch strings.ToLower(strings.TrimSpace(mode)) {
				case strings.ToLower(string(corev1.ReadWriteMany)):
					modes = append(modes, corev1.ReadWriteMany)
				case strings.ToLower(string(corev1.ReadOnlyMany)):
					modes = append(modes, corev1.ReadOnlyMany)
				default:
					modes = append(modes, corev1.ReadWriteOnce)
				}
			}
		}
		size := strings.TrimSpace(tpl.Size)
		if size == "" {
			size = "20Gi"
		}
		spec := corev1.PersistentVolumeClaimSpec{
			AccessModes: modes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
		}
		if class := strings.TrimSpace(tpl.StorageClass); class != "" {
			spec.StorageClassName = pointerString(class)
		}
		volumes = append(volumes, corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				Ephemeral: &corev1.EphemeralVolumeSource{
					VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
						ObjectMeta: metav1.ObjectMeta{Labels: workspaceLabels},
						Spec:       spec,
					},
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: workspaceMountPath})
	}
	if claim := strings.TrimSpace(template.SharedConfigClaim); claim != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "shared-config",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
			},
		})
		mountPath := strings.TrimSpace(template.SharedConfigMountPath)
		if mountPath == "" {
			mountPath = "/buckley/shared"
		}
		mounts = append(mounts, corev1.VolumeMount{Name: "shared-config", MountPath: mountPath})
	}
	if name := strings.TrimSpace(template.ConfigMap); name != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: name},
				},
			},
		})
		mountPath := strings.TrimSpace(template.ConfigMapMountPath)
		if mountPath == "" {
			mountPath = "/home/buckley/.buckley/config.yaml"
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "config",
			MountPath: mountPath,
			SubPath:   "config.yaml",
			ReadOnly:  true,
		})
	}
	if len(mounts) > 0 {
		container.VolumeMounts = mounts
	}

	podSpec := corev1.PodSpec{
		Containers:         []corev1.Container{container},
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: template.ServiceAccount,
	}
	if len(volumes) > 0 {
		podSpec.Volumes = volumes
	}
	if len(template.ImagePullSecrets) > 0 {
		secrets := make([]corev1.LocalObjectReference, 0, len(template.ImagePullSecrets))
		for _, name := range template.ImagePullSecrets {
			secrets = append(secrets, corev1.LocalObjectReference{Name: name})
		}
		podSpec.ImagePullSecrets = secrets
	}
	if len(template.NodeSelector) > 0 {
		podSpec.NodeSelector = make(map[string]string, len(template.NodeSelector))
		for k, v := range template.NodeSelector {
			podSpec.NodeSelector[k] = v
		}
	}
	if len(template.Tolerations) > 0 {
		podSpec.Tolerations = append([]corev1.Toleration{}, template.Tolerations...)
	}
	if template.Affinity != nil {
		podSpec.Affinity = template.Affinity
	}

	backoff := template.BackoffLimit
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:   jobName,
			Labels: labels,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
			BackoffLimit: &backoff,
		},
	}
	if template.TTLSecondsAfterFinished > 0 {
		ttl := template.TTLSecondsAfterFinished
		job.Spec.TTLSecondsAfterFinished = &ttl
	}
	return job
}

func (b *BatchCoordinator) templateVars(plan *Plan, task *Task, remoteBranch string) map[string]string {
	planSlug := kubeSlug(plan.ID)
	if planSlug == "" {
		planSlug = kubeSlug(plan.FeatureName)
	}
	taskSlug := kubeSlug(task.ID)
	if taskSlug == "" {
		taskSlug = kubeSlug(task.Title)
	}
	vars := map[string]string{
		"PLAN_ID":       plan.ID,
		"PLAN_SLUG":     planSlug,
		"TASK_ID":       task.ID,
		"TASK_TITLE":    task.Title,
		"TASK_TYPE":     string(task.Type),
		"TASK_SLUG":     taskSlug,
		"FEATURE":       plan.FeatureName,
		"REPO_URL":      plan.Context.GitRemoteURL,
		"GIT_BRANCH":    plan.Context.GitBranch,
		"REMOTE_BRANCH": remoteBranch,
		"REMOTE_NAME":   b.cfg.RemoteBranch.RemoteName,
		"NAMESPACE":     b.namespace,
	}
	return vars
}

func (b *BatchCoordinator) applyTemplates(values []string, vars map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	rendered := make([]string, 0, len(values))
	for _, val := range values {
		rendered = append(rendered, b.applyTemplate(val, vars))
	}
	return rendered
}

func (b *BatchCoordinator) applyTemplate(value string, vars map[string]string) string {
	rendered := value
	for key, val := range vars {
		rendered = strings.ReplaceAll(rendered, fmt.Sprintf("[[%s]]", key), val)
		rendered = strings.ReplaceAll(rendered, fmt.Sprintf("{{%s}}", key), val)
	}
	return rendered
}

func setEnvVar(env *[]corev1.EnvVar, name string, value string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	for i := range *env {
		if (*env)[i].Name == name {
			(*env)[i].Value = value
			(*env)[i].ValueFrom = nil
			return
		}
	}
	*env = append(*env, corev1.EnvVar{Name: name, Value: value})
}

func setEnvVarIfMissing(env *[]corev1.EnvVar, name string, value string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	for i := range *env {
		if (*env)[i].Name == name {
			return
		}
	}
	*env = append(*env, corev1.EnvVar{Name: name, Value: value})
}

func (b *BatchCoordinator) buildJobName(plan *Plan, task *Task) string {
	planSlug := kubeSlug(plan.ID)
	if planSlug == "" {
		planSlug = kubeSlug(plan.FeatureName)
	}
	taskSlug := kubeSlug(task.ID)
	if taskSlug == "" {
		taskSlug = kubeSlug(task.Title)
	}
	base := fmt.Sprintf("buckley-%s-%s-%d", planSlug, taskSlug, time.Now().Unix())
	return truncateIdentifier(base, 63)
}

func truncateIdentifier(value string, max int) string {
	cleaned := strings.Trim(kubeSlug(value), "-")
	if cleaned == "" {
		cleaned = "buckley"
	}
	if len(cleaned) <= max {
		return cleaned
	}
	return strings.Trim(cleaned[:max], "-")
}

func (b *BatchCoordinator) remoteBranchForTask(plan *Plan, task *Task) string {
	if !b.cfg.RemoteBranch.Enabled {
		return ""
	}
	planSlug := kubeSlug(plan.FeatureName)
	if planSlug == "" {
		planSlug = kubeSlug(plan.ID)
	}
	taskSlug := kubeSlug(task.Title)
	if taskSlug == "" {
		taskSlug = kubeSlug(task.ID)
	}
	branch := fmt.Sprintf("%s%s-%s", b.cfg.RemoteBranch.Prefix, planSlug, taskSlug)
	return strings.Trim(branch, "-/")
}

func buildKubeClient(kubeconfig string) (kubernetes.Interface, error) {
	if strings.TrimSpace(kubeconfig) != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to load kubeconfig %s: %w", kubeconfig, err)
		}
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
		}
		return client, nil
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, fmt.Errorf("failed to create in-cluster config: %w", err)
		}
		path := filepath.Join(home, ".kube", "config")
		cfg, err = clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf("failed to load kubeconfig %s: %w", path, err)
		}
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	return client, nil
}

func detectNamespace(explicit string) string {
	if ns := strings.TrimSpace(explicit); ns != "" {
		return ns
	}
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err == nil {
		if ns := strings.TrimSpace(string(data)); ns != "" {
			return ns
		}
	}
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	return "default"
}

func kubeSlug(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == '/' || r == '.':
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func pointerString(val string) *string {
	if val == "" {
		return nil
	}
	return &val
}
