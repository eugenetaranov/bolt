package playbook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRole(t *testing.T) {
	// Create temp directory structure for test role
	tmpDir := t.TempDir()
	rolesDir := filepath.Join(tmpDir, "roles")
	roleDir := filepath.Join(rolesDir, "testrole")

	// Create role directories
	require.NoError(t, os.MkdirAll(filepath.Join(roleDir, "tasks"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(roleDir, "handlers"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(roleDir, "defaults"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(roleDir, "vars"), 0755))

	// Create tasks/main.yaml
	tasksContent := `- name: Test task
  command:
    cmd: echo "hello"
`
	require.NoError(t, os.WriteFile(filepath.Join(roleDir, "tasks", "main.yaml"), []byte(tasksContent), 0644))

	// Create handlers/main.yaml
	handlersContent := `- name: Restart service
  command:
    cmd: systemctl restart test
`
	require.NoError(t, os.WriteFile(filepath.Join(roleDir, "handlers", "main.yaml"), []byte(handlersContent), 0644))

	// Create defaults/main.yaml
	defaultsContent := `default_port: 8080
default_user: nobody
`
	require.NoError(t, os.WriteFile(filepath.Join(roleDir, "defaults", "main.yaml"), []byte(defaultsContent), 0644))

	// Create vars/main.yaml
	varsContent := `service_name: testservice
config_path: /etc/test
`
	require.NoError(t, os.WriteFile(filepath.Join(roleDir, "vars", "main.yaml"), []byte(varsContent), 0644))

	// Test loading role
	role, err := LoadRole("testrole", rolesDir)
	require.NoError(t, err)

	assert.Equal(t, "testrole", role.Name)
	assert.Equal(t, roleDir, role.Path)

	// Check tasks
	require.Len(t, role.Tasks, 1)
	assert.Equal(t, "Test task", role.Tasks[0].Name)
	assert.Equal(t, "command", role.Tasks[0].Module)

	// Check handlers
	require.Len(t, role.Handlers, 1)
	assert.Equal(t, "Restart service", role.Handlers[0].Name)

	// Check defaults
	assert.Equal(t, 8080, role.Defaults["default_port"])
	assert.Equal(t, "nobody", role.Defaults["default_user"])

	// Check vars
	assert.Equal(t, "testservice", role.Vars["service_name"])
	assert.Equal(t, "/etc/test", role.Vars["config_path"])
}

func TestLoadRoleNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := LoadRole("nonexistent", tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLoadRoleEmptyRole(t *testing.T) {
	// Create temp directory with just the role directory (no files)
	tmpDir := t.TempDir()
	rolesDir := filepath.Join(tmpDir, "roles")
	roleDir := filepath.Join(rolesDir, "emptyrole")
	require.NoError(t, os.MkdirAll(roleDir, 0755))

	role, err := LoadRole("emptyrole", rolesDir)
	require.NoError(t, err)

	assert.Equal(t, "emptyrole", role.Name)
	assert.Empty(t, role.Tasks)
	assert.Empty(t, role.Handlers)
	assert.Empty(t, role.Vars)
	assert.Empty(t, role.Defaults)
}

func TestLoadRoles(t *testing.T) {
	tmpDir := t.TempDir()
	rolesDir := filepath.Join(tmpDir, "roles")

	// Create two roles
	for _, roleName := range []string{"role1", "role2"} {
		roleDir := filepath.Join(rolesDir, roleName)
		require.NoError(t, os.MkdirAll(filepath.Join(roleDir, "tasks"), 0755))
		tasksContent := `- name: Task from ` + roleName + `
  command:
    cmd: echo "` + roleName + `"
`
		require.NoError(t, os.WriteFile(filepath.Join(roleDir, "tasks", "main.yaml"), []byte(tasksContent), 0644))
	}

	roles, err := LoadRoles([]string{"role1", "role2"}, rolesDir)
	require.NoError(t, err)
	require.Len(t, roles, 2)

	assert.Equal(t, "role1", roles[0].Name)
	assert.Equal(t, "role2", roles[1].Name)
}

func TestLoadRolesEmpty(t *testing.T) {
	roles, err := LoadRoles(nil, "/tmp")
	require.NoError(t, err)
	assert.Nil(t, roles)
}

func TestMergeRoleVars(t *testing.T) {
	roles := []*Role{
		{
			Name:     "role1",
			Defaults: map[string]any{"port": 80, "user": "www"},
			Vars:     map[string]any{"port": 8080},
		},
		{
			Name:     "role2",
			Defaults: map[string]any{"timeout": 30},
			Vars:     map[string]any{"debug": true},
		},
	}

	playVars := map[string]any{
		"port":   9000, // Should override role vars
		"custom": "value",
	}

	merged := MergeRoleVars(roles, playVars)

	// Play vars have highest priority
	assert.Equal(t, 9000, merged["port"])
	assert.Equal(t, "value", merged["custom"])

	// Role vars come through if not overridden
	assert.Equal(t, true, merged["debug"])

	// Defaults come through if not overridden
	assert.Equal(t, "www", merged["user"])
	assert.Equal(t, 30, merged["timeout"])
}

func TestMergeRoleVarsEmptyRoles(t *testing.T) {
	playVars := map[string]any{
		"port": 8080,
	}

	merged := MergeRoleVars(nil, playVars)
	assert.Equal(t, 8080, merged["port"])
}

func TestExpandRoleTasks(t *testing.T) {
	roles := []*Role{
		{
			Name: "role1",
			Tasks: []*Task{
				{Name: "Role1 Task1", Module: "command"},
				{Name: "Role1 Task2", Module: "file"},
			},
		},
		{
			Name: "role2",
			Tasks: []*Task{
				{Name: "Role2 Task1", Module: "copy"},
			},
		},
	}

	playTasks := []*Task{
		{Name: "Play Task1", Module: "command"},
	}

	expanded := ExpandRoleTasks(roles, playTasks)

	require.Len(t, expanded, 4)
	assert.Equal(t, "Role1 Task1", expanded[0].Name)
	assert.Equal(t, "Role1 Task2", expanded[1].Name)
	assert.Equal(t, "Role2 Task1", expanded[2].Name)
	assert.Equal(t, "Play Task1", expanded[3].Name)
}

func TestExpandRoleHandlers(t *testing.T) {
	roles := []*Role{
		{
			Name: "role1",
			Handlers: []*Task{
				{Name: "Restart nginx", Module: "command"},
			},
		},
	}

	playHandlers := []*Task{
		{Name: "Reload app", Module: "command"},
	}

	expanded := ExpandRoleHandlers(roles, playHandlers)

	require.Len(t, expanded, 2)
	assert.Equal(t, "Restart nginx", expanded[0].Name)
	assert.Equal(t, "Reload app", expanded[1].Name)
}

func TestParsePlayWithRoles(t *testing.T) {
	yamlContent := `
name: Test Play
hosts: localhost
connection: local
roles:
  - webserver
  - database
tasks:
  - name: Final task
    command:
      cmd: echo "done"
`
	pb, err := ParseRaw([]byte(yamlContent), "test.yaml")
	require.NoError(t, err)
	require.Len(t, pb.Plays, 1)

	play := pb.Plays[0]
	assert.Equal(t, []string{"webserver", "database"}, play.Roles)
	require.Len(t, play.Tasks, 1)
	assert.Equal(t, "Final task", play.Tasks[0].Name)
}
