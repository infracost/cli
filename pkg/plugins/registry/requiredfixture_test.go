package registry

// requiredPluginsManifest mirrors pkg/plugins/required.go as a registry
// manifest, proving every compiled-in required plugin is expressible in the
// schema using the existing releases.infracost.io artifact layout. It is kept
// here (rather than imported from pkg/plugins) to avoid an import cycle: the
// installer in pkg/plugins consumes this package.
var requiredPluginsManifest = `{
  "schemaVersion": 1,
  "plugins": [
    ` + reqParser("terraform") + `,
    ` + reqParser("terragrunt") + `,
    ` + reqParser("cloudformation") + `,
    ` + reqParser("ciscostacks") + `,
    ` + reqParser("arm") + `,
    ` + reqParser("terraform-plan") + `,
    ` + reqProvider("aws") + `,
    ` + reqProvider("google") + `,
    ` + reqProvider("azure") + `,
    {
      "name": "infracost/kubernetes",
      "displayName": "Kubernetes",
      "description": "Kubernetes parser and provider.",
      "author": "infracost",
      "official": true,
      "homepage": "https://github.com/infracost/kubernetes",
      "license": "Apache-2.0",
      "versionUrl": "https://releases.infracost.io/infracost-parser-kubernetes/{os}/{arch}/latest/version",
      "components": [
        ` + reqComponent("parser", "infracost-parser-kubernetes") + `,
        ` + reqComponent("provider", "infracost-provider-kubernetes") + `
      ]
    }
  ]
}`

func reqParser(key string) string {
	return reqEntry(key, "parser", "infracost-parser-"+key)
}

func reqProvider(key string) string {
	return reqEntry(key, "provider", "infracost-provider-"+key)
}

func reqEntry(key, typ, binary string) string {
	return `{
      "name": "infracost/` + key + `",
      "displayName": "` + key + `",
      "description": "Official ` + key + ` plugin.",
      "author": "infracost",
      "official": true,
      "homepage": "https://github.com/infracost/` + key + `",
      "license": "Apache-2.0",
      "versionUrl": "https://releases.infracost.io/` + binary + `/{os}/{arch}/latest/version",
      "components": [` + reqComponent(typ, binary) + `]
    }`
}

func reqComponent(typ, binary string) string {
	return `{
          "type": "` + typ + `",
          "binaryName": "` + binary + `",
          "platforms": ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64"],
          "download": "https://releases.infracost.io/` + binary + `/{os}/{arch}/{version}/data.tar.gz",
          "checksums": "https://releases.infracost.io/` + binary + `/{os}/{arch}/{version}/data.tar.gz.sha256"
        }`
}
