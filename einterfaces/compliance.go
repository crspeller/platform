// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package einterfaces

import (
	"github.com/crspeller/mattermost-server/model"
)

type ComplianceInterface interface {
	StartComplianceDailyJob()
	RunComplianceJob(job *model.Compliance) *model.AppError
}
