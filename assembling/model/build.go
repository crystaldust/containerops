package model

type Build struct {
	Namespace  string `sql:"not null;type:varchar(255)"`
	Repository string `sql:"not null;type:varchar(255)"`
	Image      string `sql:"not null;type:varchar(255)"`
	Tag        string `sql:"not null;type:varchar(255)"`
	Logs       string `sql:"not null;type:text"`
	Succeed    bool   `sql:"not null"`
}

func (b *Build) TableName() string {
	return "assembling_build"
}
