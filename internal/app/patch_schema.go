package app

func init() {
	Schemas["patch-ports"]["status"] = enum("available", "prewired", "reserved", "awaiting-connect", "connected", "awaiting-cease", "ceased", "broken")
	for key, field := range (Schema{"duplexPeerId": ref("patch-ports", false), "switchPortId": ref("ports", false), "documentIds": {Type: "document-refs"}, "allocationId": textField(false), "lastEffectiveAt": textField(false), "assignedAt": textField(false), "connectedAt": textField(false), "ceaseRequestedAt": textField(false), "ceasedAt": textField(false)}) {
		Schemas["patch-ports"][key] = field
	}
}
