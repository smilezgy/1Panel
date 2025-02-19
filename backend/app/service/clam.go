package service

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/1Panel-dev/1Panel/backend/app/dto"
	"github.com/1Panel-dev/1Panel/backend/buserr"
	"github.com/1Panel-dev/1Panel/backend/constant"
	"github.com/1Panel-dev/1Panel/backend/global"
	"github.com/1Panel-dev/1Panel/backend/utils/cmd"
	"github.com/1Panel-dev/1Panel/backend/utils/common"
	"github.com/1Panel-dev/1Panel/backend/utils/systemctl"
	"github.com/jinzhu/copier"

	"github.com/pkg/errors"
)

const (
	clamServiceNameCentOs = "clamd@scan.service"
	clamServiceNameUbuntu = "clamav-daemon.service"
	scanDir               = "scan-result"
)

type ClamService struct {
	serviceName string
}

type IClamService interface {
	LoadBaseInfo() (dto.ClamBaseInfo, error)
	Operate(operate string) error
	SearchWithPage(search dto.SearchWithPage) (int64, interface{}, error)
	Create(req dto.ClamCreate) error
	Update(req dto.ClamUpdate) error
	Delete(ids []uint) error
	HandleOnce(req dto.OperateByID) error
	LoadFile(req dto.OperationWithName) (string, error)
	UpdateFile(req dto.UpdateByNameAndFile) error
	LoadRecords(req dto.ClamLogSearch) (int64, interface{}, error)
	CleanRecord(req dto.OperateByID) error
}

func NewIClamService() IClamService {
	return &ClamService{}
}

func (f *ClamService) LoadBaseInfo() (dto.ClamBaseInfo, error) {
	var baseInfo dto.ClamBaseInfo
	baseInfo.Version = "-"
	exist1, _ := systemctl.IsExist(clamServiceNameCentOs)
	if exist1 {
		f.serviceName = clamServiceNameCentOs
		baseInfo.IsExist = true
		baseInfo.IsActive, _ = systemctl.IsActive(clamServiceNameCentOs)
	}
	exist2, _ := systemctl.IsExist(clamServiceNameUbuntu)
	if exist2 {
		f.serviceName = clamServiceNameUbuntu
		baseInfo.IsExist = true
		baseInfo.IsActive, _ = systemctl.IsActive(clamServiceNameUbuntu)
	}

	if baseInfo.IsActive {
		version, err := cmd.Exec("clamdscan --version")
		if err != nil {
			return baseInfo, nil
		}
		if strings.Contains(version, "/") {
			baseInfo.Version = strings.TrimPrefix(strings.Split(version, "/")[0], "ClamAV ")
		} else {
			baseInfo.Version = strings.TrimPrefix(version, "ClamAV ")
		}
	}
	return baseInfo, nil
}

func (f *ClamService) Operate(operate string) error {
	switch operate {
	case "start", "restart", "stop":
		stdout, err := cmd.Execf("systemctl %s %s", operate, f.serviceName)
		if err != nil {
			return fmt.Errorf("%s the %s failed, err: %s", operate, f.serviceName, stdout)
		}
		return nil
	default:
		return fmt.Errorf("not support such operation: %v", operate)
	}
}

func (f *ClamService) SearchWithPage(req dto.SearchWithPage) (int64, interface{}, error) {
	total, commands, err := clamRepo.Page(req.Page, req.PageSize, commonRepo.WithLikeName(req.Info))
	if err != nil {
		return 0, nil, err
	}
	var datas []dto.ClamInfo
	for _, command := range commands {
		var item dto.ClamInfo
		if err := copier.Copy(&item, &command); err != nil {
			return 0, nil, errors.WithMessage(constant.ErrStructTransform, err.Error())
		}
		item.LastHandleDate = "-"
		datas = append(datas, item)
	}
	nyc, _ := time.LoadLocation(common.LoadTimeZone())
	for i := 0; i < len(datas); i++ {
		logPaths := loadFileByName(datas[i].Name)
		sort.Slice(logPaths, func(i, j int) bool {
			return logPaths[i] > logPaths[j]
		})
		if len(logPaths) != 0 {
			t1, err := time.ParseInLocation("20060102150405", logPaths[0], nyc)
			if err != nil {
				continue
			}
			datas[i].LastHandleDate = t1.Format("2006-01-02 15:04:05")
		}
	}
	return total, datas, err
}

func (f *ClamService) Create(req dto.ClamCreate) error {
	clam, _ := clamRepo.Get(commonRepo.WithByName(req.Name))
	if clam.ID != 0 {
		return constant.ErrRecordExist
	}
	if err := copier.Copy(&clam, &req); err != nil {
		return errors.WithMessage(constant.ErrStructTransform, err.Error())
	}
	if err := clamRepo.Create(&clam); err != nil {
		return err
	}
	return nil
}

func (f *ClamService) Update(req dto.ClamUpdate) error {
	clam, _ := clamRepo.Get(commonRepo.WithByName(req.Name))
	if clam.ID == 0 {
		return constant.ErrRecordNotFound
	}
	upMap := map[string]interface{}{}
	upMap["name"] = req.Name
	upMap["path"] = req.Path
	upMap["description"] = req.Description
	if err := clamRepo.Update(req.ID, upMap); err != nil {
		return err
	}
	return nil
}

func (u *ClamService) Delete(ids []uint) error {
	if len(ids) == 1 {
		clam, _ := clamRepo.Get(commonRepo.WithByID(ids[0]))
		if clam.ID == 0 {
			return constant.ErrRecordNotFound
		}
		return clamRepo.Delete(commonRepo.WithByID(ids[0]))
	}
	return clamRepo.Delete(commonRepo.WithIdsIn(ids))
}

func (u *ClamService) HandleOnce(req dto.OperateByID) error {
	clam, _ := clamRepo.Get(commonRepo.WithByID(req.ID))
	if clam.ID == 0 {
		return constant.ErrRecordNotFound
	}
	if cmd.CheckIllegal(clam.Path) {
		return buserr.New(constant.ErrCmdIllegal)
	}
	logFile := path.Join(global.CONF.System.DataDir, scanDir, clam.Name, time.Now().Format("20060102150405"))
	if _, err := os.Stat(path.Dir(logFile)); err != nil {
		_ = os.MkdirAll(path.Dir(logFile), os.ModePerm)
	}
	go func() {
		cmd := exec.Command("clamdscan", "--fdpass", clam.Path, "-l", logFile)
		_, _ = cmd.CombinedOutput()
	}()
	return nil
}

func (u *ClamService) LoadRecords(req dto.ClamLogSearch) (int64, interface{}, error) {
	clam, _ := clamRepo.Get(commonRepo.WithByID(req.ClamID))
	if clam.ID == 0 {
		return 0, nil, constant.ErrRecordNotFound
	}
	logPaths := loadFileByName(clam.Name)
	if len(logPaths) == 0 {
		return 0, nil, nil
	}

	var filterFiles []string
	nyc, _ := time.LoadLocation(common.LoadTimeZone())
	for _, item := range logPaths {
		t1, err := time.ParseInLocation("20060102150405", item, nyc)
		if err != nil {
			continue
		}
		if t1.After(req.StartTime) && t1.Before(req.EndTime) {
			filterFiles = append(filterFiles, item)
		}
	}
	if len(filterFiles) == 0 {
		return 0, nil, nil
	}

	sort.Slice(filterFiles, func(i, j int) bool {
		return filterFiles[i] > filterFiles[j]
	})

	var records []string
	total, start, end := len(filterFiles), (req.Page-1)*req.PageSize, req.Page*req.PageSize
	if start > total {
		records = make([]string, 0)
	} else {
		if end >= total {
			end = total
		}
		records = filterFiles[start:end]
	}

	var datas []dto.ClamLog
	for i := 0; i < len(records); i++ {
		item := loadResultFromLog(path.Join(global.CONF.System.DataDir, scanDir, clam.Name, records[i]))
		datas = append(datas, item)
	}
	return int64(total), datas, nil
}

func (u *ClamService) CleanRecord(req dto.OperateByID) error {
	clam, _ := clamRepo.Get(commonRepo.WithByID(req.ID))
	if clam.ID == 0 {
		return constant.ErrRecordNotFound
	}
	pathItem := path.Join(global.CONF.System.DataDir, scanDir, clam.Name)
	_ = os.RemoveAll(pathItem)
	return nil
}

func (u *ClamService) LoadFile(req dto.OperationWithName) (string, error) {
	filePath := ""
	switch req.Name {
	case "clamd":
		if u.serviceName == clamServiceNameUbuntu {
			filePath = "/etc/clamav/clamd.conf"
		} else {
			filePath = "/etc/clamd.d/scan.conf"
		}
	case "clamd-log":
		if u.serviceName == clamServiceNameUbuntu {
			filePath = "/var/log/clamav/clamav.log"
		} else {
			filePath = "/var/log/clamd.scan"
		}
	case "freshclam":
		if u.serviceName == clamServiceNameUbuntu {
			filePath = "/etc/clamav/freshclam.conf"
		} else {
			filePath = "/etc/freshclam.conf"
		}
	case "freshclam-log":
		if u.serviceName == clamServiceNameUbuntu {
			filePath = "/var/log/clamav/freshclam.log"
		} else {
			filePath = "/var/log/clamav/freshclam.log"
		}
	default:
		return "", fmt.Errorf("not support such type")
	}
	if _, err := os.Stat(filePath); err != nil {
		return "", buserr.New("ErrHttpReqNotFound")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (u *ClamService) UpdateFile(req dto.UpdateByNameAndFile) error {
	filePath := ""
	service := ""
	switch req.Name {
	case "clamd":
		if u.serviceName == clamServiceNameUbuntu {
			service = clamServiceNameUbuntu
			filePath = "/etc/clamav/clamd.conf"
		} else {
			service = clamServiceNameCentOs
			filePath = "/etc/clamd.d/scan.conf"
		}
	case "freshclam":
		if u.serviceName == clamServiceNameUbuntu {
			filePath = "/etc/clamav/freshclam.conf"
		} else {
			filePath = "/etc/freshclam.conf"
		}
		service = "clamav-freshclam.service"
	default:
		return fmt.Errorf("not support such type")
	}
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	defer file.Close()
	write := bufio.NewWriter(file)
	_, _ = write.WriteString(req.File)
	write.Flush()

	_ = systemctl.Restart(service)
	return nil
}

func loadFileByName(name string) []string {
	var logPaths []string
	pathItem := path.Join(global.CONF.System.DataDir, scanDir, name)
	_ = filepath.Walk(pathItem, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || info.Name() == name {
			return nil
		}
		logPaths = append(logPaths, info.Name())
		return nil
	})
	return logPaths
}
func loadResultFromLog(pathItem string) dto.ClamLog {
	var data dto.ClamLog
	data.Name = path.Base(pathItem)
	data.Status = constant.StatusWaiting
	file, err := os.ReadFile(pathItem)
	if err != nil {
		return data
	}
	data.Log = string(file)
	lines := strings.Split(string(file), "\n")
	for _, line := range lines {
		if strings.Contains(line, "- SCAN SUMMARY -") {
			data.Status = constant.StatusDone
		}
		if data.Status != constant.StatusDone {
			continue
		}
		switch {
		case strings.HasPrefix(line, "Infected files:"):
			data.InfectedFiles = strings.TrimPrefix(line, "Infected files:")
		case strings.HasPrefix(line, "Time:"):
			if strings.Contains(line, "(") {
				data.ScanTime = strings.ReplaceAll(strings.Split(line, "(")[1], ")", "")
				continue
			}
			data.ScanTime = strings.TrimPrefix(line, "Time:")
		case strings.HasPrefix(line, "Start Date:"):
			data.ScanDate = strings.TrimPrefix(line, "Start Date:")
		}
	}
	return data
}
