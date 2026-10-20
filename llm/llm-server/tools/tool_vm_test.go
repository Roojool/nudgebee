package tools

import (
	"net/netip"
	"strings"
	"testing"

	"nudgebee/llm/tools/core"
)

func TestVMExecuteInputFromExecutor(t *testing.T) {
	input := core.ParseNBToolCallRequestFromInput(`{"resource_id":"vm-id","command":"uname -a","datasource_id":"source-id"}`)
	resourceID, command, datasourceID := vmExecuteInput(input)
	if resourceID != "vm-id" || command != "uname -a" || datasourceID != "source-id" {
		t.Fatalf("parsed VM command = %q, %q, %q", resourceID, command, datasourceID)
	}
	input = core.ParseNBToolCallRequestFromInput(`{"resource_id":"  vm-id  ","command":"  uname -a  ","datasource_id":"  source-id  "}`)
	resourceID, command, datasourceID = vmExecuteInput(input)
	if resourceID != "vm-id" || command != "  uname -a  " || datasourceID != "source-id" {
		t.Fatalf("trimmed VM identifiers changed command: %q, %q, %q", resourceID, command, datasourceID)
	}
	input = core.ParseNBToolCallRequestFromInput(`{"resource_id":"   ","command":"   ","datasource_id":"   "}`)
	resourceID, command, datasourceID = vmExecuteInput(input)
	if resourceID != "" || strings.TrimSpace(command) != "" || datasourceID != "" {
		t.Fatalf("whitespace-only VM input = %q, %q, %q", resourceID, command, datasourceID)
	}
}

func TestVMExecuteClassifierUsesCommandField(t *testing.T) {
	prompt, err := (VMExecuteTool{}).InferToolRequestTypePrompt(nil, ToolVMExecute, `{"resource_id":"vm-id","command":"rm /tmp/file"}`)
	if err != nil || !strings.Contains(prompt, "command field") {
		t.Fatalf("classifier prompt = %q, %v", prompt, err)
	}
}

func TestSSHWriteConfirmationScopesExactOperation(t *testing.T) {
	vm := VMExecuteTool{}
	first := `{"resource_id":"vm-1","command":"systemctl restart nginx"}`
	vmKey := vm.ConfirmationKey(first)
	if vmKey != vm.ConfirmationKey(" "+first+" ") ||
		vm.ConfirmationKey(first) == vm.ConfirmationKey(`{"resource_id":"vm-2","command":"systemctl restart nginx"}`) ||
		vm.ConfirmationKey(first) == vm.ConfirmationKey(`{"resource_id":"vm-1","command":"systemctl restart nginx","datasource_id":"ssh-2"}`) ||
		vm.ConfirmationKey(first) == vm.ConfirmationKey(`{"resource_id":"vm-1","command":"rm -rf /tmp/cache"}`) {
		t.Fatal("VM approval must be stable for replay and distinct for another target or command")
	}
	if question := vm.ConfirmationQuestion(first); !strings.Contains(question, "vm-1") || !strings.Contains(question, "systemctl restart nginx") {
		t.Fatalf("VM approval question omits target or command: %q", question)
	}

	server := ServerExecuteTool{}
	serverInput := `{"instance":"vm-1","args":"systemctl restart nginx","command":"shell"}`
	if server.ConfirmationKey(serverInput) == server.ConfirmationKey(`{"instance":"vm-2","args":"systemctl restart nginx","command":"shell"}`) ||
		server.ConfirmationKey(serverInput) == server.ConfirmationKey(`{"instance":"vm-1","host_name":"192.0.2.2","args":"systemctl restart nginx","command":"shell"}`) ||
		server.ConfirmationKey(serverInput) == server.ConfirmationKey(`{"instance":"vm-1","args":"rm -rf /tmp/cache","command":"shell"}`) {
		t.Fatal("server SSH approval must be distinct for another target or command")
	}
	if question := server.ConfirmationQuestion(serverInput); !strings.Contains(question, "vm-1") || !strings.Contains(question, "systemctl restart nginx") {
		t.Fatalf("server SSH approval question omits target or command: %q", question)
	}
	prompt, err := server.InferToolRequestTypePrompt(nil, server.Name(), serverInput)
	if err != nil || !strings.Contains(prompt, "args field") {
		t.Fatalf("server SSH classifier must inspect args: %v, %q", err, prompt)
	}
}

func TestVMSSHSourceSelection(t *testing.T) {
	ip := netip.MustParseAddr("10.0.1.5")
	publicIP := netip.MustParseAddr("203.0.113.5")
	qualified := vmSSHSource{ID: "a", Name: "segment-a", Key: "local:a", Labels: []byte(`{"mode":"dynamic","allowed_hosts":["10.0.1.0/24"]}`)}
	other := vmSSHSource{ID: "b", Name: "segment-b", Key: "local:b", Labels: []byte(`{"mode":"dynamic","allowed_hosts":["10.0.2.0/24"]}`)}
	public := vmSSHSource{ID: "c", Name: "public", Key: "local:c", Labels: []byte(`{"mode":"dynamic","allowed_hosts":["203.0.113.5"]}`)}
	if got, selectedIP, err := chooseVMSSHSource([]vmSSHSource{qualified, other}, "", []netip.Addr{ip}); err != nil || got.ID != "a" || selectedIP != ip {
		t.Fatalf("selection = %q, %v", got.ID, err)
	}
	if got, selectedIP, err := chooseVMSSHSource([]vmSSHSource{qualified, public}, "c", []netip.Addr{ip, publicIP}); err != nil || got.ID != "c" || selectedIP != publicIP {
		t.Fatalf("explicit public selection = %q at %v, %v", got.ID, selectedIP, err)
	}
	if got, selectedIP, err := chooseVMSSHSource([]vmSSHSource{qualified, public}, "", []netip.Addr{ip, publicIP}); err != nil || got.ID != "a" || selectedIP != ip {
		t.Fatalf("preferred private selection = %q at %v, %v", got.ID, selectedIP, err)
	}
	if got, selectedIP, err := chooseVMSSHSource([]vmSSHSource{public}, "", []netip.Addr{ip, publicIP}); err != nil || got.ID != "c" || selectedIP != publicIP {
		t.Fatalf("public fallback = %q at %v, %v", got.ID, selectedIP, err)
	}
	if _, _, err := chooseVMSSHSource([]vmSSHSource{qualified, qualified, public}, "", []netip.Addr{ip, publicIP}); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("overlap should be ambiguous: %v", err)
	}
	if _, _, err := chooseVMSSHSource([]vmSSHSource{qualified}, "b", []netip.Addr{ip}); err == nil || !strings.Contains(err.Error(), "not enabled and linked") {
		t.Fatalf("explicit missing datasource must fail: %v", err)
	}
	if _, _, err := chooseVMSSHSource([]vmSSHSource{qualified}, "a", []netip.Addr{publicIP}); err == nil || !strings.Contains(err.Error(), "does not permit") {
		t.Fatalf("explicit ineligible datasource must fail: %v", err)
	}
	withoutKey := qualified
	withoutKey.Key = ""
	if _, _, err := chooseVMSSHSource([]vmSSHSource{withoutKey}, "a", []netip.Addr{ip}); err == nil || !strings.Contains(err.Error(), "no datasource key") {
		t.Fatalf("explicit datasource with no key must fail: %v", err)
	}
	for _, labels := range []string{`{"mode":"dynamic"}`, `{"mode":"static","allowed_hosts":["10.0.1.5"]}`, `{"mode":"dynamic","allowed_hosts":["10.0.2.0/24"]}`} {
		if vmSSHAllows([]byte(labels), ip) {
			t.Errorf("unexpected allow for %s", labels)
		}
	}
}

func TestVMAddressRequiresInventoryAddress(t *testing.T) {
	addrs, err := vmAddresses(vmTarget{ResourceID: "vm-10.0.1.5"})
	if err != nil || len(addrs) != 1 || addrs[0].String() != "10.0.1.5" {
		t.Fatalf("addresses = %v, %v", addrs, err)
	}
	addrs, err = vmAddresses(vmTarget{ResourceID: "native-instance", Meta: []byte(`{"PrivateIpAddress":"10.0.1.5","PublicIpAddress":"203.0.113.5"}`)})
	if err != nil || len(addrs) != 2 || addrs[1].String() != "203.0.113.5" {
		t.Fatalf("private/public addresses = %v, %v", addrs, err)
	}
	if _, err := vmAddresses(vmTarget{ResourceID: "vm-invalid", Meta: []byte(`{}`)}); err == nil {
		t.Fatal("invalid inventory address should fail")
	}
}
