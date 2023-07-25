package sheets

import (
	"context"
	"fmt"
	googlesheets "google.golang.org/api/sheets/v4"
	"jiaming2012/sales-processor/models"
)

type sheets struct {
	sheetsSrv         *googlesheets.Service
	parentDirectoryID string
}

func NewClient(sheetsSrv *googlesheets.Service) *sheets {
	return &sheets{
		sheetsSrv: sheetsSrv,
	}
}

func (s *sheets) FetchRows(ctx context.Context, spreadsheetId string, sheetName string, cells string) (models.Rows, error) {
	sheetRange := fmt.Sprintf("%s!%s", sheetName, cells)
	response, err := s.sheetsSrv.Spreadsheets.Values.Get(spreadsheetId, sheetRange).Context(ctx).Do()
	if err != nil || response.HTTPStatusCode != 200 {
		return nil, err
	}

	return response.Values, nil
}
