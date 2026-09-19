package service

import (
	"testing"
)

func TestEffectivePermissionNodes_PlatformAdminShortCircuit(t *testing.T) {
	set := EffectivePermissionNodes("platform_admin", nil, []PermOverride{
		{Node: "instance.read", Effect: "deny"},
	}, true)
	if len(set) != len(AllPermissionNodes()) {
		t.Fatalf("platform admin should have all nodes, got %d", len(set))
	}
	if _, ok := set["instance.read"]; !ok {
		t.Fatal("platform admin must keep instance.read despite deny override")
	}
}

func TestEffectivePermissionNodes_DenyWins(t *testing.T) {
	set := EffectivePermissionNodes("group_operator", []string{"instance.read", "log.read", "terminal.access"},
		[]PermOverride{
			{Node: "terminal.access", Effect: "deny"},
			{Node: "stats.read", Effect: "allow"},
		}, false)
	if _, ok := set["terminal.access"]; ok {
		t.Fatal("deny must remove terminal.access")
	}
	if _, ok := set["log.read"]; !ok {
		t.Fatal("template node log.read should remain")
	}
	if _, ok := set["stats.read"]; !ok {
		t.Fatal("allow override should add stats.read")
	}
	if _, ok := set["user.manage"]; ok {
		t.Fatal("operator must not gain platform user.manage")
	}
}

func TestSystemRoleSeeds_ContainExpected(t *testing.T) {
	seed := SystemRoleSeed()
	if _, ok := seed["group_viewer"]; !ok {
		t.Fatal("missing group_viewer seed")
	}
	viewer := seed["group_viewer"].Nodes
	for _, n := range viewer {
		switch n {
		case "file.write", "terminal.access", "instance.operate", "user.manage":
			t.Fatalf("viewer seed should not include write node %s", n)
		}
	}
	op := seed["group_operator"].Nodes
	found := false
	for _, n := range op {
		if n == "instance.operate" {
			found = true
		}
		if n == "group.member.write" {
			t.Fatal("operator must not manage group members")
		}
		if n == "instance.launchspec.write" {
			t.Fatal("operator must not have launchspec write")
		}
	}
	if !found {
		t.Fatal("operator seed missing instance.operate")
	}
}

func TestIsValidPermissionNode(t *testing.T) {
	if !IsValidPermissionNode("rbac.manage") {
		t.Fatal("rbac.manage should be valid")
	}
	if IsValidPermissionNode("not.a.real.node") {
		t.Fatal("unknown node should be invalid")
	}
}
