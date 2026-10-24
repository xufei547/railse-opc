package autostart

import (
	"path/filepath"
	"testing"
)

func TestNames(t *testing.T) {
	if Label("agent") != "dev.railse.agent" || Label("hub") != "dev.railse.hub" {
		t.Fatalf("Label: %s %s", Label("agent"), Label("hub"))
	}
	if p := PlistPath("/Users/u", "hub"); p != "/Users/u/Library/LaunchAgents/dev.railse.hub.plist" {
		t.Fatalf("PlistPath: %s", p)
	}
	if n := UnitName("agent"); n != "railse-agent.service" {
		t.Fatalf("UnitName: %s", n)
	}
	if p := UnitPath("/home/u", "agent", false); p != "/home/u/.config/systemd/user/railse-agent.service" {
		t.Fatalf("用户单元: %s", p)
	}
	if p := UnitPath("/home/u", "hub", true); p != "/etc/systemd/system/railse-hub.service" {
		t.Fatalf("系统单元: %s", p)
	}
	old := SystemUnitDir
	defer func() { SystemUnitDir = old }()
	SystemUnitDir = "/tmp/x"
	if p := UnitPath("/home/u", "hub", true); p != filepath.Join("/tmp/x", "railse-hub.service") {
		t.Fatalf("换目录后的系统单元: %s", p)
	}
}
