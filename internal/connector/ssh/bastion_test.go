package ssh

import "testing"

func TestParseProxyJump(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want *BastionConfig
	}{
		{"empty", "", nil},
		{"none", "none", nil},
		{"host only", "bastion.example.com", &BastionConfig{Host: "bastion.example.com"}},
		{"user and host", "ec2-user@bastion", &BastionConfig{User: "ec2-user", Host: "bastion"}},
		{"user host port", "admin@10.0.0.5:2222", &BastionConfig{User: "admin", Host: "10.0.0.5", Port: 2222}},
		{"host port", "jump:2200", &BastionConfig{Host: "jump", Port: 2200}},
		{"chain uses first hop", "a@h1,b@h2", &BastionConfig{User: "a", Host: "h1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseProxyJump(tt.spec)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("expected nil, got %+v", got)
			case tt.want == nil:
				return
			case got == nil:
				t.Fatalf("expected %+v, got nil", tt.want)
			}
			if *got != *tt.want {
				t.Errorf("parseProxyJump(%q) = %+v, want %+v", tt.spec, got, tt.want)
			}
		})
	}
}

// WithProxyJump should populate the bastion config from a spec.
func TestWithProxyJump(t *testing.T) {
	c := New("target", WithProxyJump("jump@bastion:2222"))
	if c.bastionCfg == nil {
		t.Fatal("expected bastionCfg to be set")
	}
	if c.bastionCfg.Host != "bastion" || c.bastionCfg.User != "jump" || c.bastionCfg.Port != 2222 {
		t.Errorf("unexpected bastion config: %+v", c.bastionCfg)
	}
}
