package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowStep struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	With            map[string]string `yaml:"with"`
	Env             map[string]string `yaml:"env"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

type workflowJob struct {
	Needs           []string          `yaml:"needs"`
	If              string            `yaml:"if"`
	Uses            string            `yaml:"uses"`
	With            map[string]string `yaml:"with"`
	Outputs         map[string]string `yaml:"outputs"`
	Permissions     map[string]string `yaml:"permissions"`
	Secrets         string            `yaml:"secrets"`
	Steps           []workflowStep    `yaml:"steps"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

type workflowDocument struct {
	On   yaml.Node              `yaml:"on"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

func readWorkflow(t *testing.T, name string) workflowDocument {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowDocument
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("解析 %s: %v", name, err)
	}
	return workflow
}

func containsNeed(needs []string, want string) bool {
	for _, need := range needs {
		if need == want {
			return true
		}
	}
	return false
}

func checkoutStep(t *testing.T, steps []workflowStep) workflowStep {
	t.Helper()
	for _, step := range steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			return step
		}
	}
	t.Fatal("缺少源码 checkout")
	return workflowStep{}
}

func TestReleaseWorkflowsGateEveryPublishedCommit(t *testing.T) {
	for file, publishJobs := range map[string][]string{"release.yml": {"build", "docker"}} {
		t.Run(file, func(t *testing.T) {
			workflow := readWorkflow(t, file)
			resolve, ok := workflow.Jobs["resolve"]
			if !ok {
				t.Fatal("发布入口必须先解析目标为固定提交 SHA")
			}
			if resolve.Permissions["contents"] != "read" || resolve.Outputs["commit_sha"] != "${{ steps.commit.outputs.sha }}" {
				t.Fatal("提交解析必须只读并输出实际 checkout 的 SHA")
			}
			if got := checkoutStep(t, resolve.Steps).With["ref"]; got != "${{ inputs.commit_sha || github.sha }}" {
				t.Fatalf("提交解析未保留自定义 ref 和缺省事件 SHA: %q", got)
			}
			gate, ok := workflow.Jobs["quality"]
			if !ok || !containsNeed(gate.Needs, "resolve") || gate.Uses != "./.github/workflows/quality-checks.yml" || gate.With["commit_sha"] != "${{ needs.resolve.outputs.commit_sha }}" {
				t.Fatal("完整质量检查必须使用解析后的同一提交")
			}
			if gate.ContinueOnError || gate.If != "" || gate.Permissions["contents"] != "read" {
				t.Fatal("质量门禁不得绕过失败或拥有发布权限")
			}
			for _, publishJob := range publishJobs {
				publish := workflow.Jobs[publishJob]
				if !containsNeed(publish.Needs, "resolve") || !containsNeed(publish.Needs, "quality") || publish.ContinueOnError {
					t.Fatal("发布 job 必须依赖解析和完整质量检查成功，不能忽略失败")
				}
				selector := "build_package"
				if publishJob == "docker" {
					selector = "build_image"
				}
				if publish.If != "github.event_name == 'push' || inputs."+selector {
					t.Fatal("发布只能按产物选择开关过滤，不得用 always 或成功状态表达式绕过门禁")
				}
				if containsNeed(publish.Needs, "build") || containsNeed(publish.Needs, "docker") {
					t.Fatal("包和镜像应在质量检查后并行构建")
				}
				if got := checkoutStep(t, publish.Steps).With["ref"]; got != "${{ needs.resolve.outputs.commit_sha }}" {
					t.Fatalf("发布重新解析了可移动 ref，未使用已验证 SHA: %q", got)
				}
			}
			for _, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if strings.Contains(step.Run, "${{") {
						t.Fatalf("%s 直接向 shell 注入表达式；应经 env 传入", step.Name)
					}
				}
			}
		})
	}
}

func TestManualReleaseEntrypointsKeepTheSharedQualityGate(t *testing.T) {
	for file, selected := range map[string]string{"build.yml": "build_package", "build-image.yml": "build_image"} {
		t.Run(file, func(t *testing.T) {
			workflow := readWorkflow(t, file)
			var events map[string]yaml.Node
			if err := workflow.On.Decode(&events); err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events["workflow_dispatch"].Kind == 0 {
				t.Fatal("旧入口只保留手动触发，标签发布由统一流水线负责")
			}
			if len(workflow.Jobs) != 1 {
				t.Fatal("手动入口应委托统一流水线，不另建可绕过的发布 job")
			}
			call := workflow.Jobs["release"]
			if call.Uses != "./.github/workflows/release.yml" || call.Secrets != "inherit" || call.If != "" || call.ContinueOnError {
				t.Fatal("手动入口必须完整委托受质量门禁保护的流水线")
			}
			if call.With["commit_sha"] != "${{ inputs.commit_sha }}" || call.With["version"] != "${{ inputs.version }}" || call.With[selected] != "true" {
				t.Fatal("手动选择的 ref、版本和产物必须完整传递")
			}
			other := "build_image"
			if selected == other {
				other = "build_package"
			}
			if call.With[other] != "false" {
				t.Fatal("独立入口只发布所选产物")
			}
		})
	}
}

func TestPrereleaseImagesDoNotReplaceLatest(t *testing.T) {
	workflow := readWorkflow(t, "release.yml")
	latestRules := 0
	for _, step := range workflow.Jobs["docker"].Steps {
		if !strings.HasPrefix(step.Uses, "docker/metadata-action@") {
			continue
		}
		for _, rule := range strings.Split(step.With["tags"], "\n") {
			if !strings.Contains(rule, "value=latest") {
				continue
			}
			latestRules++
			if !strings.Contains(rule, "enable=${{ inputs.release_type != '预发布版（pre-release）' && (") {
				t.Fatal("预发布镜像不得覆盖 latest；所有事件条件都必须受发布类型保护")
			}
		}
	}
	if latestRules != 1 {
		t.Fatal("必须只有一条受保护的 latest 镜像标签规则")
	}
}

func TestReusableQualityChecksUseRequestedCommitAndKeepAllGates(t *testing.T) {
	workflow := readWorkflow(t, "quality-checks.yml")
	var events struct {
		WorkflowCall struct {
			Inputs map[string]struct {
				Required bool   `yaml:"required"`
				Type     string `yaml:"type"`
			} `yaml:"inputs"`
		} `yaml:"workflow_call"`
	}
	if err := workflow.On.Decode(&events); err != nil {
		t.Fatal(err)
	}
	input := events.WorkflowCall.Inputs["commit_sha"]
	if !input.Required || input.Type != "string" {
		t.Fatal("复用质量检查必须要求固定 commit_sha")
	}
	var commands strings.Builder
	for _, name := range []string{"test", "docker"} {
		job := workflow.Jobs[name]
		if job.If != "" || job.ContinueOnError {
			t.Fatalf("质量检查 %s 不得跳过或忽略失败", name)
		}
		if got := checkoutStep(t, job.Steps).With["ref"]; got != "${{ inputs.commit_sha || github.sha }}" {
			t.Fatalf("%s 未检出请求的提交: %q", name, got)
		}
		verified := false
		for _, step := range job.Steps {
			if step.ContinueOnError {
				t.Fatalf("%s 不得忽略检查失败", step.Name)
			}
			if step.Env["EXPECTED_COMMIT"] == "${{ inputs.commit_sha }}" && strings.Contains(step.Run, "git rev-parse HEAD") && strings.Contains(step.Run, "$EXPECTED_COMMIT") {
				verified = true
			}
			commands.WriteString(step.Run)
			commands.WriteByte('\n')
		}
		if !verified {
			t.Fatalf("%s 必须核验检出 SHA", name)
		}
	}
	for _, required := range []string{"node --check", "node --test", "python3 -m unittest discover -s tests -p test_ncmm_update_script.py", "go vet ./...", "go test ./...", "go test -race", "./internal/selfupdate", "./internal/filelock", "./internal/atomicfile", "tests/webui_smoke.js", "tests/webui_music_partner.js", "tests/webui_cookie_status.js", "tests/webui_task_config.js", "tests/webui_mobile_config.js", "docker compose config --quiet", "docker build", "--network none"} {
		if !strings.Contains(commands.String(), required) {
			t.Errorf("发布质量门禁缺少既有检查: %s", required)
		}
	}
}
