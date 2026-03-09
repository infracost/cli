package plugins

type Manifest struct {
	Plugins map[string]Plugin `json:"plugins"`
}

type Plugin struct {
	Latest   string             `json:"latest,omitempty"`
	Versions map[string]Version `json:"versions,omitempty"`
}

type Version struct {
	Artifacts map[string]Artifact `json:"artifacts,omitempty"`
}

type Artifact struct {
	URL  string `json:"url,omitempty"`
	SHA  string `json:"sha,omitempty"`
	Name string `json:"name,omitempty"`
}
