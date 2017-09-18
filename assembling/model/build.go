package model

import (
	"encoding/json"
	"fmt"
)

type Build struct {
	Registry  string `sql:"not null;type:varchar(255)"`
	Namespace string `sql:"not null;type:varchar(255)"`
	Image     string `sql:"not null;type:varchar(255)"`
	Tag       string `sql:"not null;type:varchar(255)"`
	BuildLogs string `sql:"not null;type:text"`
	PushLogs  string `sql:"not null; type:text"`
	Succeed   bool   `sql:"not null"`
}

func (b *Build) Save() error {
	tx := DB.Begin()
	bs, _ := json.MarshalIndent(b, "", "  ")
	fmt.Println("going to save:")
	fmt.Println(string(bs))

	if err := tx.Debug().Create(&b).Error; err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	return nil
}

func (b *Build) TableName() string {
	return "assembling_build"
}
