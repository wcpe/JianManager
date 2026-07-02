package model

import "time"

type ClientSecurityProfile struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	ChannelID       string    `gorm:"column:channel_id;type:varchar(64);not null;uniqueIndex:idx_client_security_profile_identity" json:"channelId"`
	MachineID       string    `gorm:"column:machine_id;type:varchar(128);not null;uniqueIndex:idx_client_security_profile_identity" json:"machineId"`
	InstallID       string    `gorm:"column:install_id;type:varchar(128);not null;uniqueIndex:idx_client_security_profile_identity" json:"installId"`
	PlayerName      string    `gorm:"type:varchar(64)" json:"playerName"`
	PlayerNameNorm  string    `gorm:"type:varchar(64);index" json:"playerNameNorm"`
	KeyID           uint      `gorm:"column:key_id;index" json:"keyId"`
	KeyPrefix       string    `gorm:"column:key_prefix;type:varchar(16)" json:"keyPrefix"`
	FirstSeen       time.Time `gorm:"column:first_seen;index" json:"firstSeen"`
	LastSeen        time.Time `gorm:"column:last_seen;index" json:"lastSeen"`
	LastIP          string    `gorm:"column:last_ip;type:varchar(64);index" json:"lastIp"`
	UserAgent       string    `gorm:"type:varchar(255)" json:"userAgent"`
	CoreVersion     string    `gorm:"type:varchar(64)" json:"coreVersion"`
	WedgeVersion    string    `gorm:"type:varchar(64)" json:"wedgeVersion"`
	ManifestVersion string    `gorm:"type:varchar(64)" json:"manifestVersion"`
	OS              string    `gorm:"type:varchar(64)" json:"os"`
	OSVersion       string    `gorm:"type:varchar(64)" json:"osVersion"`
	Arch            string    `gorm:"type:varchar(32)" json:"arch"`
	JavaVendor      string    `gorm:"type:varchar(128)" json:"javaVendor"`
	JavaVersion     string    `gorm:"type:varchar(64)" json:"javaVersion"`
	JavaArch        string    `gorm:"type:varchar(32)" json:"javaArch"`
	Launcher        string    `gorm:"type:varchar(128)" json:"launcher"`
	Locale          string    `gorm:"type:varchar(32)" json:"locale"`
	Timezone        string    `gorm:"type:varchar(64)" json:"timezone"`
	MemoryTier      string    `gorm:"type:varchar(32)" json:"memoryTier"`
	RiskScore       int       `gorm:"default:0;not null" json:"riskScore"`
	RiskLevel       string    `gorm:"type:varchar(32);default:normal;not null" json:"riskLevel"`
	ProtectionState string    `gorm:"type:varchar(32);default:normal;not null" json:"protectionState"`
	LabelsJSON      string    `gorm:"column:labels_json;type:text" json:"-"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ClientSecurityHello struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ChannelID   string    `gorm:"column:channel_id;type:varchar(64);index" json:"channelId"`
	MachineID   string    `gorm:"column:machine_id;type:varchar(128);index" json:"machineId"`
	InstallID   string    `gorm:"column:install_id;type:varchar(128);index" json:"installId"`
	PlayerName  string    `gorm:"type:varchar(64);index" json:"playerName"`
	Accepted    bool      `gorm:"default:false;not null" json:"accepted"`
	ErrCode     string    `gorm:"column:err_code;type:varchar(64);index" json:"errCode"`
	IP          string    `gorm:"type:varchar(64);index" json:"ip"`
	KeyID       uint      `gorm:"column:key_id;index" json:"keyId"`
	KeyPrefix   string    `gorm:"column:key_prefix;type:varchar(16)" json:"keyPrefix"`
	UserAgent   string    `gorm:"type:varchar(255)" json:"userAgent"`
	PayloadJSON string    `gorm:"column:payload_json;type:text" json:"-"`
	CreatedAt   time.Time `gorm:"index" json:"createdAt"`
}

type ClientSecurityRiskEvent struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	SubjectType  string    `gorm:"column:subject_type;type:varchar(32);index" json:"subjectType"`
	SubjectValue string    `gorm:"column:subject_value;type:varchar(128);index" json:"subjectValue"`
	ChannelID    string    `gorm:"column:channel_id;type:varchar(64);index" json:"channelId"`
	MachineID    string    `gorm:"column:machine_id;type:varchar(128);index" json:"machineId"`
	InstallID    string    `gorm:"column:install_id;type:varchar(128);index" json:"installId"`
	PlayerName   string    `gorm:"type:varchar(64);index" json:"playerName"`
	IP           string    `gorm:"type:varchar(64);index" json:"ip"`
	KeyID        uint      `gorm:"column:key_id;index" json:"keyId"`
	KeyPrefix    string    `gorm:"column:key_prefix;type:varchar(16)" json:"keyPrefix"`
	RuleCode     string    `gorm:"column:rule_code;type:varchar(64);default:'';not null;index" json:"ruleCode"`
	Severity     string    `gorm:"type:varchar(32);default:info;not null" json:"severity"`
	ScoreDelta   int       `gorm:"column:score_delta;default:0;not null" json:"scoreDelta"`
	Action       string    `gorm:"type:varchar(32)" json:"action"`
	Reason       string    `gorm:"type:varchar(512)" json:"reason"`
	DetailJSON   string    `gorm:"column:detail_json;type:text" json:"-"`
	CreatedAt    time.Time `gorm:"index" json:"createdAt"`
}

type ClientProtectionAction struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	TargetType  string     `gorm:"type:varchar(32);not null;index" json:"targetType"`
	TargetValue string     `gorm:"column:target_value;type:varchar(128);default:'';not null;index" json:"targetValue"`
	ChannelID   string     `gorm:"column:channel_id;type:varchar(64);index" json:"channelId"`
	Action      string     `gorm:"type:varchar(64);not null;index" json:"action"`
	Status      string     `gorm:"type:varchar(16);default:active;not null;index" json:"status"`
	PolicyJSON  string     `gorm:"column:policy_json;type:text" json:"-"`
	Reason      string     `gorm:"type:varchar(512)" json:"reason"`
	Auto        bool       `gorm:"default:false;not null" json:"auto"`
	ExpiresAt   *time.Time `gorm:"index" json:"expiresAt"`
	CreatedBy   uint       `gorm:"default:0;not null" json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	CanceledAt  *time.Time `json:"canceledAt"`
}

type ClientSecurityGroup struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	Name             string    `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Kind             string    `gorm:"type:varchar(16);default:manual;not null" json:"kind"`
	TargetType       string    `gorm:"column:target_type;type:varchar(32);default:'';not null" json:"targetType"`
	RuleJSON         string    `gorm:"column:rule_json;type:text" json:"-"`
	ActionPolicyJSON string    `gorm:"column:action_policy_json;type:text" json:"-"`
	Enabled          bool      `gorm:"default:true;not null" json:"enabled"`
	CreatedBy        uint      `gorm:"default:0;not null" json:"createdBy"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type ClientSecurityCounter struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Scope     string    `gorm:"type:varchar(32);not null;uniqueIndex:idx_client_security_counter_bucket" json:"scope"`
	Key       string    `gorm:"type:varchar(128);not null;uniqueIndex:idx_client_security_counter_bucket" json:"key"`
	Bucket    string    `gorm:"type:varchar(32);not null;uniqueIndex:idx_client_security_counter_bucket" json:"bucket"`
	Value     int64     `gorm:"default:0;not null" json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}
