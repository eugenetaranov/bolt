package playbook

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tackhq/tack/internal/source"
)

func TestIsRemoteRoleRef(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want bool
	}{
		{"git ssh", "git@github.com:org/repo.git", true},
		{"https git", "https://github.com/org/repo.git", true},
		{"http", "http://example.com/repo.git", true},
		{"s3", "s3://bucket/roles/webserver", true},
		{"bare local name", "webserver", false},
		{"relative path", "../shared-roles/webserver", false},
		{"absolute path", "/opt/roles/webserver", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isRemoteRoleRef(tc.ref))
		})
	}
}

func TestDeriveRemoteRoleName(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want string
	}{
		{"whole repo https", "https://github.com/org/ansible-role-nginx.git", "ansible-role-nginx"},
		{"whole repo ssh", "git@github.com:org/ansible-role-nginx.git", "ansible-role-nginx"},
		{"in-repo path", "git@github.com:org/repo.git//roles/webserver", "webserver"},
		{"in-repo path trailing slash", "https://github.com/org/repo.git//roles/webserver/", "webserver"},
		{"whole-repo dot path", "https://github.com/org/repo.git//.", "repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, deriveRemoteRoleName(tc.ref))
		})
	}
}

func TestResolveRemoteRoleSource_WholeRepoFallback(t *testing.T) {
	// No "//path" separator: source.Resolve alone would error; the role
	// loader must retry treating the whole repo as the role.
	src, err := resolveRemoteRoleSource("git@github.com:org/nginx-role.git")
	require.NoError(t, err)
	gitSrc, ok := src.(*source.GitSource)
	require.True(t, ok, "expected *source.GitSource, got %T", src)
	assert.Equal(t, "git@github.com:org/nginx-role.git", gitSrc.RepoURL)
	assert.Equal(t, ".", gitSrc.Path)
}

func TestResolveRemoteRoleSource_ExplicitPathPreserved(t *testing.T) {
	src, err := resolveRemoteRoleSource("git@github.com:org/repo.git//roles/webserver")
	require.NoError(t, err)
	gitSrc, ok := src.(*source.GitSource)
	require.True(t, ok)
	assert.Equal(t, "roles/webserver", gitSrc.Path)
}

// --- fake source.Source for testing LoadRole/LoadRoles' remote branch
// without touching the network or invoking git ---

type fakeRoleSource struct {
	fetchPath  string
	fetchErr   error
	cleanupted bool
}

func (f *fakeRoleSource) Fetch(context.Context) (string, func(), error) {
	if f.fetchErr != nil {
		return "", nil, f.fetchErr
	}
	return f.fetchPath, func() { f.cleanupted = true }, nil
}

// writeFakeRoleDir builds a minimal on-disk role directory (just
// tasks/main.yaml) under dir and returns dir.
func writeFakeRoleDir(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", "main.yaml"), []byte(`- name: from remote role
  command:
    cmd: echo remote
`), 0644))
	return dir
}

// withFakeRoleSource overrides resolveRemoteRoleSource for the duration of
// the test, restoring the original on cleanup.
func withFakeRoleSource(t *testing.T, fn func(ref string) (source.Source, error)) {
	t.Helper()
	orig := resolveRemoteRoleSource
	resolveRemoteRoleSource = fn
	t.Cleanup(func() { resolveRemoteRoleSource = orig })
}

func TestLoadRole_Remote_Success(t *testing.T) {
	roleDir := writeFakeRoleDir(t, t.TempDir())
	fake := &fakeRoleSource{fetchPath: roleDir}
	withFakeRoleSource(t, func(string) (source.Source, error) { return fake, nil })

	role, cleanup, err := LoadRole(context.Background(), "https://github.com/org/nginx-role.git", "/unused/roles")
	require.NoError(t, err)
	require.NotNil(t, role)

	assert.Equal(t, "nginx-role", role.Name)
	assert.Equal(t, roleDir, role.Path)
	require.Len(t, role.Tasks, 1)
	assert.Equal(t, "from remote role", role.Tasks[0].Name)
	assert.Equal(t, roleDir, role.Tasks[0].RolePath)
	assert.Equal(t, "nginx-role", role.Tasks[0].RoleName)

	assert.False(t, fake.cleanupted, "cleanup must not run until the caller invokes it")
	cleanup()
	assert.True(t, fake.cleanupted)
}

func TestLoadRole_Remote_FetchError(t *testing.T) {
	fake := &fakeRoleSource{fetchErr: assert.AnError}
	withFakeRoleSource(t, func(string) (source.Source, error) { return fake, nil })

	_, _, err := LoadRole(context.Background(), "https://github.com/org/nginx-role.git", "/unused/roles")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fetch")
}

func TestLoadRole_Remote_FetchedPathIsFile_CleansUp(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-role")
	require.NoError(t, os.WriteFile(filePath, []byte("oops"), 0644))
	fake := &fakeRoleSource{fetchPath: filePath}
	withFakeRoleSource(t, func(string) (source.Source, error) { return fake, nil })

	_, _, err := LoadRole(context.Background(), "https://github.com/org/not-a-role.git", "/unused/roles")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
	assert.True(t, fake.cleanupted, "cleanup must still run when the fetched path is invalid")
}

func TestLoadRole_Remote_FetchedPathMissing_CleansUp(t *testing.T) {
	fake := &fakeRoleSource{fetchPath: filepath.Join(t.TempDir(), "does-not-exist")}
	withFakeRoleSource(t, func(string) (source.Source, error) { return fake, nil })

	_, _, err := LoadRole(context.Background(), "https://github.com/org/gone.git", "/unused/roles")
	require.Error(t, err)
	assert.True(t, fake.cleanupted)
}

func TestLoadRoles_Remote_AggregatesCleanup(t *testing.T) {
	dir1 := writeFakeRoleDir(t, filepath.Join(t.TempDir(), "r1"))
	dir2 := writeFakeRoleDir(t, filepath.Join(t.TempDir(), "r2"))
	fake1 := &fakeRoleSource{fetchPath: dir1}
	fake2 := &fakeRoleSource{fetchPath: dir2}

	calls := 0
	withFakeRoleSource(t, func(string) (source.Source, error) {
		calls++
		if calls == 1 {
			return fake1, nil
		}
		return fake2, nil
	})

	roles, cleanup, err := LoadRoles(context.Background(), []RoleRef{
		{Name: "https://github.com/org/role-one.git"},
		{Name: "https://github.com/org/role-two.git"},
	}, "/unused/roles")
	require.NoError(t, err)
	require.Len(t, roles, 2)

	cleanup()
	assert.True(t, fake1.cleanupted)
	assert.True(t, fake2.cleanupted)
}

func TestLoadRoles_Remote_ErrorRollsBackEarlierCleanups(t *testing.T) {
	dir1 := writeFakeRoleDir(t, filepath.Join(t.TempDir(), "r1"))
	fake1 := &fakeRoleSource{fetchPath: dir1}
	fake2 := &fakeRoleSource{fetchErr: assert.AnError}

	calls := 0
	withFakeRoleSource(t, func(string) (source.Source, error) {
		calls++
		if calls == 1 {
			return fake1, nil
		}
		return fake2, nil
	})

	_, _, err := LoadRoles(context.Background(), []RoleRef{
		{Name: "https://github.com/org/role-one.git"},
		{Name: "https://github.com/org/role-two.git"},
	}, "/unused/roles")
	require.Error(t, err)
	assert.True(t, fake1.cleanupted, "the first role's temp dir must be cleaned up when a later role fails to load")
}

func TestLoadRoles_Remote_TagsApplied(t *testing.T) {
	roleDir := writeFakeRoleDir(t, t.TempDir())
	fake := &fakeRoleSource{fetchPath: roleDir}
	withFakeRoleSource(t, func(string) (source.Source, error) { return fake, nil })

	roles, cleanup, err := LoadRoles(context.Background(), []RoleRef{
		{Name: "https://github.com/org/nginx-role.git", Tags: []string{"web"}},
	}, "/unused/roles")
	require.NoError(t, err)
	defer cleanup()

	require.Len(t, roles, 1)
	require.Len(t, roles[0].Tasks, 1)
	assert.Contains(t, roles[0].Tasks[0].Tags, "web")
}
