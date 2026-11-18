package dto

type WidgetConfig struct {
	SchemaVersion   int64   `json:"schema_version"`
	Name            string  `json:"name"`
	AvatarAssetID   *string `json:"avatar_asset_id"`
	Greeting        string  `json:"greeting"`
	PrimaryColor    string  `json:"primary_color"`
	BackgroundColor string  `json:"background_color"`
	MessageColor    string  `json:"message_color"`
	TextColor       string  `json:"text_color"`
	FontFamily      string  `json:"font_family"`
	FontSize        int64   `json:"font_size"`
	Radius          int64   `json:"radius"`
	ButtonShape     string  `json:"button_shape"`
	Position        string  `json:"position"`
	OffsetX         int64   `json:"offset_x"`
	OffsetY         int64   `json:"offset_y"`
	IconID          string  `json:"icon_id"`
	Placeholder     string  `json:"placeholder"`
	ButtonLabel     string  `json:"button_label"`
}

type WidgetSettings struct {
	ConfigRevision          int64        `json:"config_revision"`
	PublishedConfigRevision int64        `json:"published_config_revision"`
	Draft                   WidgetConfig `json:"draft"`
}

type Asset struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	DetectedType string `json:"detected_type"`
	Size         int64  `json:"size"`
}

type WidgetSessionCreate struct {
	AgentPublicID string `json:"agent_public_id"`
}

type WidgetSession struct {
	SessionToken   string       `json:"session_token"`
	ExpiresAt      string       `json:"expires_at"`
	ConversationID string       `json:"conversation_id"`
	ConfigRevision int64        `json:"config_revision"`
	WidgetConfig   WidgetConfig `json:"widget_config"`
	ConsentVersion string       `json:"consent_version"`
	ConsentText    string       `json:"consent_text"`
}
