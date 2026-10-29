package steamprogress

import "time"

// Achievement is an observation from the official Web API, never a manual value.
type Achievement struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Icon        string     `json:"icon"`
	IconGray    string     `json:"icon_gray"`
	Unlocked    bool       `json:"unlocked"`
	UnlockedAt  *time.Time `json:"unlocked_at"`
}

// Progress is scoped to one Steam account and app. Nil achievements means unknown;
// an empty (non-nil) list means an explicitly confirmed empty schema.
type Progress struct {
	CredentialTag          string        `json:"-"`
	AccountID              string        `json:"account_id"`
	ObservedAt             time.Time     `json:"observed_at"`
	HoursPlayed            float64       `json:"hours_played"`
	Achievements           []Achievement `json:"achievements"`
	AchievementsObservedAt *time.Time    `json:"achievements_observed_at"`
}

func (p Progress) Counts() (unlocked, total *int) {
	if p.Achievements == nil {
		return nil, nil
	}
	n, count := 0, len(p.Achievements)
	for _, a := range p.Achievements {
		if a.Unlocked {
			n++
		}
	}
	return &n, &count
}
