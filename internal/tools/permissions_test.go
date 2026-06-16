// Package tools 验证权限层在 plan mode 下的关键放行与拦截语义。
// 这些测试锁住源码仓库风格的规划态权限边界，避免后续迁移把计划文件编辑能力又误伤掉。
package tools

import "testing"

// TestCheckPermissionAllowsEditingPlanFileInPlanMode 验证 plan mode 下允许对计划文件使用 write_file 和 edit_file。
// 这样模型在规划态里既能整段写入，也能增量修改计划文件，与源码仓库保持一致。
func TestCheckPermissionAllowsEditingPlanFileInPlanMode(t *testing.T) {
	planFilePath := "C:\\plans\\plan-123.md"
	permissionContext := PermissionContext{
		PlanFilePath: planFilePath,
	}

	writeDecision := CheckPermission(
		PermissionPlan,
		Tool{Name: "write_file"},
		Invocation{Name: "write_file", Arguments: map[string]string{"file_path": planFilePath}},
		permissionContext,
	)
	if writeDecision.Action != "allow" {
		t.Fatalf("write_file decision = %+v, want allow", writeDecision)
	}

	editDecision := CheckPermission(
		PermissionPlan,
		Tool{Name: "edit_file"},
		Invocation{Name: "edit_file", Arguments: map[string]string{"file_path": planFilePath}},
		permissionContext,
	)
	if editDecision.Action != "allow" {
		t.Fatalf("edit_file decision = %+v, want allow", editDecision)
	}
}

// TestCheckPermissionDeniesNonPlanEditsInPlanMode 验证 plan mode 下仍会阻止对非计划文件的编辑。
// 这能确保我们放开计划文件编辑后，没有顺带把整个项目写权限都放开。
func TestCheckPermissionDeniesNonPlanEditsInPlanMode(t *testing.T) {
	decision := CheckPermission(
		PermissionPlan,
		Tool{Name: "edit_file"},
		Invocation{Name: "edit_file", Arguments: map[string]string{"file_path": "C:\\repo\\main.go"}},
		PermissionContext{PlanFilePath: "C:\\plans\\plan-123.md"},
	)
	if decision.Action != "deny" {
		t.Fatalf("decision = %+v, want deny", decision)
	}
	if decision.Message != "Blocked in plan mode: edit_file" {
		t.Fatalf("decision.Message = %q, want %q", decision.Message, "Blocked in plan mode: edit_file")
	}
}

// TestCheckPermissionDeniesShellInPlanMode 验证 plan mode 下 shell 会返回与源码仓库一致的专用拦截文案。
// 这样 REPL 和模型都能看到更明确的拒绝原因，而不是泛化成普通工具被阻止。
func TestCheckPermissionDeniesShellInPlanMode(t *testing.T) {
	decision := CheckPermission(
		PermissionPlan,
		Tool{Name: "run_shell"},
		Invocation{Name: "run_shell", Arguments: map[string]string{"command": "dir"}},
		PermissionContext{PlanFilePath: "C:\\plans\\plan-123.md"},
	)
	if decision.Action != "deny" {
		t.Fatalf("decision = %+v, want deny", decision)
	}
	if decision.Message != "Shell commands blocked in plan mode" {
		t.Fatalf("decision.Message = %q, want %q", decision.Message, "Shell commands blocked in plan mode")
	}
}
