package app

import "time"

const (
	maxHouseMembers = 8
	maxHouseRooms   = 4
)

type Config struct {
	DemoPassword         string
	DemoPasswordRequired bool
	EmbeddingEnabled     bool
	Addr                 string
	DatabasePath         string
	StaticDir            string
	AllowedOrigins       []string
	SecureCookies        bool
	LiveKitURL           string
	LiveKitAPIKey        string
	LiveKitSecret        string
	MetricsToken         string
}

type HouseSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RoomCount   int    `json:"roomCount"`
	MemberCount int    `json:"memberCount"`
	LastRoomID  string `json:"lastRoomId"`
}

type Room struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Position int    `json:"position"`
	Game     *Game  `json:"game,omitempty"`
}

type Game struct {
	Provider            string `json:"provider"`
	URL                 string `json:"url"`
	CoordinatorMemberID string `json:"coordinatorMemberId"`
	Revision            int64  `json:"revision"`
}

type Member struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Role         string `json:"role"`
	ActiveRoomID string `json:"activeRoomId"`
	Connected    bool   `json:"connected"`
	JoinedAt     int64  `json:"joinedAt"`
}

type JoinRequest struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
	ExpiresAt   int64  `json:"expiresAt"`
}

type Permissions struct {
	IsHost         bool `json:"isHost"`
	CanManageHouse bool `json:"canManageHouse"`
}

type HouseSnapshot struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	HostMemberID    string        `json:"hostMemberId"`
	Revision        int64         `json:"revision"`
	PresenceVersion int64         `json:"presenceVersion"`
	ExpiresAt       int64         `json:"expiresAt"`
	InviteToken     string        `json:"inviteToken,omitempty"`
	Rooms           []Room        `json:"rooms"`
	Members         []Member      `json:"members"`
	PendingRequests []JoinRequest `json:"pendingRequests,omitempty"`
	SelfMemberID    string        `json:"selfMemberId"`
	Permissions     Permissions   `json:"permissions"`
	MediaGeneration int64         `json:"mediaGeneration"`
}

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
	Details   any    `json:"details,omitempty"`
}

type errorEnvelope struct {
	Error APIError `json:"error"`
}

type dataEnvelope struct {
	Data any `json:"data"`
}

type session struct {
	ID        string
	TokenHash string
	ExpiresAt time.Time
}

type contextKey string

const sessionContextKey contextKey = "roomcade-session"
