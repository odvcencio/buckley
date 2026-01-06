package orchestrator

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBatchCoordinatorBuildJobRendersTemplates(t *testing.T) {
	cfg := config.BatchConfig{
		Enabled:   true,
		Namespace: "default",
		RemoteBranch: config.BatchRemoteBranchConfig{
			Enabled:    true,
			Prefix:     "automation/",
			RemoteName: "origin",
		},
		JobTemplate: config.BatchJobTemplateConfig{
			Image:              "ghcr.io/test/buckley:latest",
			ImagePullPolicy:    "IfNotPresent",
			ServiceAccount:     "buckley",
			Command:            []string{"buckley"},
			Args:               []string{"execute-task", "--plan", "[[PLAN_ID]]", "--task", "[[TASK_ID]]", "--remote-branch", "[[REMOTE_BRANCH]]"},
			Env:                map[string]string{"REMOTE_BRANCH": "[[REMOTE_BRANCH]]"},
			WorkspaceClaim:     "buckley-workspace",
			WorkspaceMountPath: "/workspace",
			BackoffLimit:       1,
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("500m"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
			EnvFromSecrets:    []string{"buckley-env"},
			EnvFromConfigMaps: []string{"buckley-configmap"},
			NodeSelector: map[string]string{
				"kubernetes.io/os": "linux",
			},
			Tolerations: []corev1.Toleration{
				{
					Key:      "dedicated",
					Operator: corev1.TolerationOpEqual,
					Value:    "buckley",
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{},
			},
			ConfigMap:          "buckley-config",
			ConfigMapMountPath: "/home/buckley/.buckley/config.yaml",
		},
	}

	bc := &BatchCoordinator{
		cfg:       cfg,
		namespace: "default",
		client:    fake.NewSimpleClientset(),
	}

	plan := &Plan{ID: "plan-1", FeatureName: "Demo", Context: PlanContext{GitBranch: "main"}}
	task := &Task{ID: "task-1", Title: "Implement", Type: TaskTypeImplementation}

	vars := bc.templateVars(plan, task, "automation/demo-implement")
	job := bc.buildJob("buckley-plan-1", vars)

	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != cfg.JobTemplate.Image {
		t.Fatalf("expected image %s, got %s", cfg.JobTemplate.Image, container.Image)
	}

	if want := plan.ID; container.Args[2] != want {
		t.Fatalf("expected plan id arg to be %s, got %s", want, container.Args[2])
	}

	foundRemote := false
	for _, env := range container.Env {
		if env.Name == "BUCKLEY_REMOTE_BRANCH" {
			foundRemote = env.Value == "automation/demo-implement"
		}
	}
	if !foundRemote {
		t.Fatal("expected remote branch env var to be rendered")
	}

	if len(job.Spec.Template.Spec.Volumes) == 0 {
		t.Fatal("expected workspace volume to be configured")
	}

	if got := container.Resources.Limits[corev1.ResourceCPU]; got.String() != "500m" {
		t.Fatalf("expected cpu limit 500m, got %s", got.String())
	}
	if got := container.Resources.Requests[corev1.ResourceMemory]; got.String() != "256Mi" {
		t.Fatalf("expected memory request 256Mi, got %s", got.String())
	}
	if job.Spec.Template.Spec.NodeSelector["kubernetes.io/os"] != "linux" {
		t.Fatalf("expected node selector to be set")
	}
	if len(job.Spec.Template.Spec.Tolerations) != 1 || job.Spec.Template.Spec.Tolerations[0].Value != "buckley" {
		t.Fatalf("expected toleration to be set")
	}
	if job.Spec.Template.Spec.Affinity == nil || job.Spec.Template.Spec.Affinity.NodeAffinity == nil {
		t.Fatalf("expected affinity to be set")
	}
	if len(container.EnvFrom) != 2 {
		t.Fatalf("expected envFrom sources to be set")
	}
	foundConfigVolume := false
	for _, vol := range job.Spec.Template.Spec.Volumes {
		if vol.Name == "config" && vol.ConfigMap != nil && vol.ConfigMap.Name == "buckley-config" {
			foundConfigVolume = true
		}
	}
	if !foundConfigVolume {
		t.Fatalf("expected configmap volume to be configured")
	}
	foundConfigMount := false
	for _, mount := range container.VolumeMounts {
		if mount.Name == "config" && mount.MountPath == "/home/buckley/.buckley/config.yaml" && mount.SubPath == "config.yaml" {
			foundConfigMount = true
		}
	}
	if !foundConfigMount {
		t.Fatalf("expected configmap mount to be configured")
	}
}

func TestBatchCoordinatorRemoteBranchDisabled(t *testing.T) {
	bc := &BatchCoordinator{
		cfg: config.BatchConfig{
			Enabled: true,
			RemoteBranch: config.BatchRemoteBranchConfig{
				Enabled: false,
			},
		},
		client:    fake.NewSimpleClientset(),
		namespace: "default",
	}

	plan := &Plan{ID: "plan-1", FeatureName: "Demo"}
	task := &Task{ID: "task-1", Title: "Task"}

	if branch := bc.remoteBranchForTask(plan, task); branch != "" {
		t.Fatalf("expected remote branch to be empty, got %s", branch)
	}
}
