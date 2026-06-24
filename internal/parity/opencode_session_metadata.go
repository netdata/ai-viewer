package parity

import (
	"database/sql"
	"encoding/json"
	"net/url"
)

type opencodeSessionMetadataIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	AgentName       string `json:"agent_name,omitempty"`
	ModelID         string `json:"model_id,omitempty"`
	ProviderID      string `json:"provider_id,omitempty"`
	Variant         string `json:"variant,omitempty"`
	Version         string `json:"version,omitempty"`
	Slug            string `json:"slug,omitempty"`
	Title           string `json:"title,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	DirectorySHA256 string `json:"directory_sha256,omitempty"`
}

type opencodeSessionModel struct {
	ID         string `json:"id"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant"`
}

func (m opencodeSessionModel) modelID() string {
	if m.ID != "" {
		return m.ID
	}
	return m.ModelID
}

func (s *opencodeSourceState) opencodeSessionMetadata(session opencodeSourceSession) (Artifact, bool, error) {
	identity := opencodeSessionMetadataFromSource(session)
	if !identity.hasDescriptiveOpencodeMetadata() {
		return Artifact{}, false, nil
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          opencodeFormat,
		sourceFile:       s.dbPath,
		nativeSessionID:  session.ID,
		nativeArtifactID: "session:" + session.ID + ":metadata",
		class:            ClassSessionMetadata,
		selectorURI:      opencodeSourceSelector("session", session.ID) + "#metadata",
		identity:         identity,
	})
	return artifact, true, err
}

func opencodeSessionMetadataFromSource(session opencodeSourceSession) opencodeSessionMetadataIdentity {
	model := decodeOpencodeSessionModel(session.Model)
	identity := opencodeSessionMetadataIdentity{
		NativeSessionID: session.ID,
		AgentName:       session.Agent,
		ModelID:         model.modelID(),
		ProviderID:      model.ProviderID,
		Variant:         model.Variant,
		Version:         session.Version,
		Slug:            session.Slug,
		Title:           session.Title,
		ProjectID:       session.ProjectID,
	}
	if session.Directory != "" {
		identity.DirectorySHA256 = stringSHA256(session.Directory)
	}
	return identity
}

func decodeOpencodeSessionModel(raw sql.NullString) opencodeSessionModel {
	if !raw.Valid || raw.String == "" {
		return opencodeSessionModel{}
	}
	var model opencodeSessionModel
	if json.Unmarshal([]byte(raw.String), &model) != nil {
		return opencodeSessionModel{}
	}
	return model
}

func artifactFromOpencodeSessionMetadata(row canonicalSessionRow) (Artifact, bool, error) {
	identity, ok, err := opencodeSessionMetadataFromCanonical(row)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		nativeSessionID:    row.nativeSessionID,
		nativeArtifactID:   "session:" + row.nativeSessionID + ":metadata",
		class:              ClassSessionMetadata,
		selectorURI:        "canonical://sessions/" + url.PathEscape(row.sessionID) + "#metadata",
		identity:           identity,
	})
	return artifact, true, err
}

func opencodeSessionMetadataFromCanonical(row canonicalSessionRow) (opencodeSessionMetadataIdentity, bool, error) {
	fields, err := jsonObjectFromNullString(row.extrasJSON, "opencode session extras")
	if err != nil {
		return opencodeSessionMetadataIdentity{}, false, err
	}
	identity := opencodeSessionMetadataIdentity{
		NativeSessionID: row.nativeSessionID,
		AgentName:       nullString(row.agentName),
		ModelID:         nullString(row.model),
		ProviderID:      nullString(row.providerAlias),
	}
	if identity.Variant, err = stringJSONField(fields, "variant"); err != nil {
		return opencodeSessionMetadataIdentity{}, false, err
	}
	if identity.Version, err = stringJSONField(fields, "version"); err != nil {
		return opencodeSessionMetadataIdentity{}, false, err
	}
	if identity.Slug, err = stringJSONField(fields, "slug"); err != nil {
		return opencodeSessionMetadataIdentity{}, false, err
	}
	if identity.Title, err = stringJSONField(fields, "title"); err != nil {
		return opencodeSessionMetadataIdentity{}, false, err
	}
	if identity.ProjectID, err = stringJSONField(fields, "project_id"); err != nil {
		return opencodeSessionMetadataIdentity{}, false, err
	}
	directory := nullString(row.cwd)
	if directory == "" {
		directory, err = stringJSONField(fields, "directory")
		if err != nil {
			return opencodeSessionMetadataIdentity{}, false, err
		}
	}
	if directory != "" {
		identity.DirectorySHA256 = stringSHA256(directory)
	}
	if !identity.hasDescriptiveOpencodeMetadata() {
		return opencodeSessionMetadataIdentity{}, false, nil
	}
	return identity, true, nil
}

func (i opencodeSessionMetadataIdentity) hasDescriptiveOpencodeMetadata() bool {
	return i.AgentName != "" ||
		i.ModelID != "" ||
		i.ProviderID != "" ||
		i.Variant != "" ||
		i.Version != "" ||
		i.Slug != "" ||
		i.Title != "" ||
		i.ProjectID != "" ||
		i.DirectorySHA256 != ""
}
