package http

import (
	"encoding/csv"
	stderrors "errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	"github.com/xuri/excelize/v2"

	"github.com/sjzar/chatlog/internal/errors"
)

type databaseRowVisitor func(columns []string, values []interface{}) error
type databaseRowStream func(visit databaseRowVisitor) (int64, error)

var errStopDatabaseStream = stderrors.New("database stream page complete")

func isDatabaseExportFormat(format string) bool {
	return format == "csv" || format == "xlsx" || format == "excel"
}

func collectDatabaseRowPage(stream databaseRowStream, limit, offset int) ([]map[string]interface{}, bool, error) {
	if limit <= 0 {
		limit = maxDatabaseResultRows
	}
	if offset < 0 {
		offset = 0
	}

	rows := make([]map[string]interface{}, 0, minIntLocal(limit+1, 256))
	seen := 0
	_, err := stream(func(columns []string, values []interface{}) error {
		if seen < offset {
			seen++
			return nil
		}
		seen++
		row := make(map[string]interface{}, len(columns))
		for index, column := range columns {
			if index >= len(values) {
				break
			}
			row[column] = cloneDatabaseCell(values[index])
		}
		rows = append(rows, row)
		if len(rows) > limit {
			return errStopDatabaseStream
		}
		return nil
	})
	if err != nil && !stderrors.Is(err, errStopDatabaseStream) {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

func cloneDatabaseCell(value interface{}) interface{} {
	if data, ok := value.([]byte); ok {
		return append([]byte(nil), data...)
	}
	return value
}

func (s *Service) exportDatabaseRows(c *gin.Context, format, filename string, stream databaseRowStream) {
	c.Header("X-Chatlog-Export-Mode", "streamed")
	var err error
	if format == "csv" {
		err = streamDatabaseCSV(c, filename, stream)
	} else {
		err = streamDatabaseXLSX(c, filename, stream)
	}
	if err == nil {
		return
	}
	if !c.Writer.Written() {
		errors.Err(c, err)
		return
	}
	log.Warn().Err(err).Str("format", format).Msg("stream database export ended with an error")
}

func streamDatabaseCSV(c *gin.Context, filename string, stream databaseRowStream) error {
	writer := csv.NewWriter(c.Writer)
	wroteHeader := false
	rowCount := 0
	_, err := stream(func(columns []string, values []interface{}) error {
		if !wroteHeader {
			setCSVExportHeaders(c, filename)
			if err := writer.Write(columns); err != nil {
				return err
			}
			wroteHeader = true
		}
		record := make([]string, len(columns))
		for index := range columns {
			if index < len(values) && values[index] != nil {
				record[index] = fmt.Sprint(values[index])
			}
		}
		if err := writer.Write(record); err != nil {
			return err
		}
		rowCount++
		if rowCount%256 == 0 {
			writer.Flush()
			if err := writer.Error(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !wroteHeader {
		setCSVExportHeaders(c, filename)
		c.Status(http.StatusOK)
	}
	writer.Flush()
	return writer.Error()
}

func setCSVExportHeaders(c *gin.Context, filename string) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.csv", filename))
	c.Header("Cache-Control", "no-cache")
}

func streamDatabaseXLSX(c *gin.Context, filename string, stream databaseRowStream) error {
	file := excelize.NewFile()
	defer func() {
		if err := file.Close(); err != nil {
			log.Warn().Err(err).Msg("close streamed database workbook")
		}
	}()

	var (
		sheetNumber  int
		rowNumber    int
		streamWriter *excelize.StreamWriter
		headers      []string
	)
	startSheet := func(columns []string) error {
		if len(columns) > excelize.MaxColumns {
			return fmt.Errorf("database export has %d columns; xlsx supports %d", len(columns), excelize.MaxColumns)
		}
		sheetNumber++
		sheetName := "Sheet" + strconv.Itoa(sheetNumber)
		if sheetNumber > 1 {
			if _, err := file.NewSheet(sheetName); err != nil {
				return err
			}
		}
		writer, err := file.NewStreamWriter(sheetName)
		if err != nil {
			return err
		}
		streamWriter = writer
		headers = append(headers[:0], columns...)
		headerValues := make([]interface{}, len(headers))
		for index := range headers {
			headerValues[index] = headers[index]
		}
		if err := streamWriter.SetRow("A1", headerValues); err != nil {
			return err
		}
		rowNumber = 2
		return nil
	}

	_, err := stream(func(columns []string, values []interface{}) error {
		if streamWriter == nil {
			if err := startSheet(columns); err != nil {
				return err
			}
		}
		if rowNumber > excelize.TotalRows {
			if err := streamWriter.Flush(); err != nil {
				return err
			}
			if err := startSheet(headers); err != nil {
				return err
			}
		}
		cell, err := excelize.CoordinatesToCellName(1, rowNumber)
		if err != nil {
			return err
		}
		if err := streamWriter.SetRow(cell, values); err != nil {
			return err
		}
		rowNumber++
		return nil
	})
	if err != nil {
		return err
	}
	if streamWriter != nil {
		if err := streamWriter.Flush(); err != nil {
			return err
		}
	}

	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.xlsx", filename))
	return file.Write(c.Writer)
}
