package controllers

import (
	"net/http"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// staffBalance is the running payable balance for one staff member.
// Positive = the business still owes the staff salary; negative = the staff
// owes a payback because deductions + advances exceeded what they earned.
type staffBalance struct {
	Balance              float64 `json:"balance"`
	PayableFromPayrolls  float64 `json:"payable_from_payrolls"`
	EarnedUnpaid         float64 `json:"earned_unpaid"`
	UncoveredPayableDays float64 `json:"uncovered_payable_days"`
	SalaryPaid           float64 `json:"salary_paid"`
	AdvancesPending      float64 `json:"advances_pending"`
	DeductionsPending    float64 `json:"deductions_pending"`
	ExtrasAddPending     float64 `json:"extras_add_pending"`
	ExtrasDeductPending  float64 `json:"extras_deduct_pending"`
}

// staffDailyRate mirrors payableSalary: monthly staff earn salary/30 per
// payable day; daily/hourly staff treat the salary as the per-day rate.
func staffDailyRate(staff models.Staff) float64 {
	if staff.SalaryType == "monthly" {
		return staff.Salary / 30
	}
	return staff.Salary
}

// payableWeight is the paid-days value of one attendance record, matching the
// PayableDays rule in computePayrollPeriodData.
func payableWeight(status string) float64 {
	switch status {
	case "present", "paid_leave", "weekly_off":
		return 1
	case "half_day":
		return 0.5
	}
	return 0
}

// payableDaysInRange counts attendance rows and payable days whose date falls
// inside [startStr, endStr] (YYYY-MM-DD, inclusive).
func payableDaysInRange(attendances []models.Attendance, startStr, endStr string) (workingDays int, payableDays float64) {
	for _, a := range attendances {
		ds := a.Date.Format("2006-01-02")
		if ds < startStr || ds > endStr {
			continue
		}
		workingDays++
		payableDays += payableWeight(a.Status)
	}
	return
}

// computeStaffBalance builds the balance from already-fetched records so the
// same math serves the per-staff endpoint and the all-staff list.
//
// The balance is built from four parts:
//   - every payroll's unsettled remainder: payable + bonus - deductions for the
//     period, minus paid_amount actually paid out. Fully paid payrolls leave 0
//     unless the recorded net was clamped at 0 (excess deductions become staff
//     payback). Pending and partially paid payrolls contribute their unsettled
//     remainder.
//   - payable salary for attendance days not covered by any payroll period.
//   - minus advance amounts still pending recovery.
//   - minus active deductions not yet folded into a payroll period.
//   - plus pending "add" extras owed to staff, minus pending "deduct" extras
//     the staff owes.
func computeStaffBalance(staff models.Staff, payrolls []models.Payroll, attendances []models.Attendance, advancesPending float64, deductions []models.StaffDeduction, extrasAddPending, extrasDeductPending float64) staffBalance {
	var result staffBalance

	coveredDates := map[string]bool{}
	markCovered := func(p models.Payroll) {
		for d := p.StartDate; !d.After(p.EndDate); d = d.AddDate(0, 0, 1) {
			coveredDates[d.Format("2006-01-02")] = true
		}
	}
	paidShare := func(p models.Payroll) float64 {
		paid := p.PaidAmount
		if p.Status == "paid" && paid <= 0 {
			paid = p.NetSalary // legacy row paid before paid_amount existed
		}
		return paid
	}

	// Pass 1: period payrolls earn payable salary for all attendance in their
	// range and cover those dates.
	for _, p := range payrolls {
		if p.IsSettlement {
			continue
		}
		startStr := p.StartDate.Format("2006-01-02")
		endStr := p.EndDate.Format("2006-01-02")
		workingDays, payableDays := payableDaysInRange(attendances, startStr, endStr)
		data := payrollPeriodData{WorkingDays: workingDays, PayableDays: payableDays}
		result.PayableFromPayrolls += payableSalary(p.BasicSalary, staff.SalaryType, data) + p.Bonus - p.Deductions
		result.SalaryPaid += paidShare(p)
		markCovered(p)
	}

	// Pass 2: settlement payrolls earn payable only for attendance days still
	// uncovered by any period payroll (or an earlier settlement) in their
	// range, then cover those dates too.
	for _, p := range payrolls {
		if !p.IsSettlement {
			continue
		}
		startStr := p.StartDate.Format("2006-01-02")
		endStr := p.EndDate.Format("2006-01-02")
		var payableDays float64
		for _, a := range attendances {
			ds := a.Date.Format("2006-01-02")
			if coveredDates[ds] || ds < startStr || ds > endStr {
				continue
			}
			payableDays += payableWeight(a.Status)
		}
		result.PayableFromPayrolls += payableDays*staffDailyRate(staff) + p.Bonus - p.Deductions
		result.SalaryPaid += paidShare(p)
		markCovered(p)
	}

	for _, a := range attendances {
		if !coveredDates[a.Date.Format("2006-01-02")] {
			result.UncoveredPayableDays += payableWeight(a.Status)
		}
	}
	result.EarnedUnpaid = result.UncoveredPayableDays * staffDailyRate(staff)

	result.AdvancesPending = advancesPending

	for _, d := range deductions {
		if coveredDates[d.DeductionDate.Format("2006-01-02")] {
			continue // already folded into a payroll's deductions
		}
		result.DeductionsPending += d.Amount
	}

	result.ExtrasAddPending = extrasAddPending
	result.ExtrasDeductPending = extrasDeductPending

	result.Balance = result.PayableFromPayrolls + result.EarnedUnpaid -
		result.SalaryPaid - result.AdvancesPending - result.DeductionsPending +
		result.ExtrasAddPending - result.ExtrasDeductPending
	return result
}

// pendingExtras sums unsettled extra amounts for one staff member, split by
// direction: add = owed to staff, deduct = owed by staff.
func pendingExtras(userID, staffID uuid.UUID) (add, deduct float64) {
	var extras []models.StaffExtraAmount
	utils.DB.Where("user_id = ? AND staff_id = ? AND status = ?", userID, staffID, "pending").Find(&extras)
	for _, e := range extras {
		if e.Direction == "add" {
			add += e.Amount
		} else {
			deduct += e.Amount
		}
	}
	return
}

// pendingAdvances sums unrecovered advance amounts for one staff member.
func pendingAdvances(userID, staffID uuid.UUID) float64 {
	var pending float64
	utils.DB.Model(&models.StaffAdvancePayment{}).
		Where("user_id = ? AND staff_id = ? AND status IN ? AND pending_amount > 0",
			userID, staffID, []string{"pending", "partial"}).
		Select("COALESCE(SUM(pending_amount), 0)").
		Scan(&pending)
	return pending
}

// activeDeductions returns deductions not yet consumed (status active); ones
// inside a payroll period are filtered later by computeStaffBalance.
func activeDeductions(userID, staffID uuid.UUID) []models.StaffDeduction {
	var deductions []models.StaffDeduction
	utils.DB.Where("user_id = ? AND staff_id = ? AND status = ?", userID, staffID, "active").Find(&deductions)
	return deductions
}

// GetStaffBalance returns the balance for one staff member.
// GET /staff/:id/balance
func GetStaffBalance(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	staffID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid staff id"})
		return
	}

	var staff models.Staff
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, staffID).First(&staff).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Staff not found"})
		return
	}

	var payrolls []models.Payroll
	utils.DB.Where("user_id = ? AND staff_id = ?", userID, staffID).
		Order("start_date ASC").
		Find(&payrolls)

	var attendances []models.Attendance
	utils.DB.Where("user_id = ? AND staff_id = ?", userID, staffID).Find(&attendances)

	extrasAdd, extrasDeduct := pendingExtras(userID, staffID)
	result := computeStaffBalance(staff, payrolls, attendances, pendingAdvances(userID, staffID), activeDeductions(userID, staffID), extrasAdd, extrasDeduct)

	c.JSON(http.StatusOK, gin.H{
		"staff_id":               staffID,
		"balance":                result.Balance,
		"payable_from_payrolls":  result.PayableFromPayrolls,
		"earned_unpaid":          result.EarnedUnpaid,
		"uncovered_payable_days": result.UncoveredPayableDays,
		"salary_paid":            result.SalaryPaid,
		"advances_pending":       result.AdvancesPending,
		"deductions_pending":     result.DeductionsPending,
		"extras_add_pending":     result.ExtrasAddPending,
		"extras_deduct_pending":  result.ExtrasDeductPending,
	})
}

// GetStaffBalances returns {staff_id: balance} for every staff member, used by
// the staff list table. Loads each record type once and groups in memory.
// GET /staff/balances
func GetStaffBalances(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var staffs []models.Staff
	utils.DB.Where("user_id = ?", userID).Find(&staffs)

	var payrolls []models.Payroll
	utils.DB.Where("user_id = ?", userID).Find(&payrolls)
	payrollsByStaff := map[uuid.UUID][]models.Payroll{}
	for _, p := range payrolls {
		payrollsByStaff[p.StaffID] = append(payrollsByStaff[p.StaffID], p)
	}

	var attendances []models.Attendance
	utils.DB.Where("user_id = ?", userID).Find(&attendances)
	attendanceByStaff := map[uuid.UUID][]models.Attendance{}
	for _, a := range attendances {
		attendanceByStaff[a.StaffID] = append(attendanceByStaff[a.StaffID], a)
	}

	var advances []models.StaffAdvancePayment
	utils.DB.Where("user_id = ? AND status IN ? AND pending_amount > 0",
		userID, []string{"pending", "partial"}).Find(&advances)
	advancesByStaff := map[uuid.UUID]float64{}
	for _, a := range advances {
		advancesByStaff[a.StaffID] += a.PendingAmount
	}

	var deductions []models.StaffDeduction
	utils.DB.Where("user_id = ? AND status = ?", userID, "active").Find(&deductions)
	deductionsByStaff := map[uuid.UUID][]models.StaffDeduction{}
	for _, d := range deductions {
		deductionsByStaff[d.StaffID] = append(deductionsByStaff[d.StaffID], d)
	}

	var extras []models.StaffExtraAmount
	utils.DB.Where("user_id = ? AND status = ?", userID, "pending").Find(&extras)
	extrasAddByStaff := map[uuid.UUID]float64{}
	extrasDeductByStaff := map[uuid.UUID]float64{}
	for _, e := range extras {
		if e.Direction == "add" {
			extrasAddByStaff[e.StaffID] += e.Amount
		} else {
			extrasDeductByStaff[e.StaffID] += e.Amount
		}
	}

	balances := gin.H{}
	for _, s := range staffs {
		result := computeStaffBalance(
			s,
			payrollsByStaff[s.ID],
			attendanceByStaff[s.ID],
			advancesByStaff[s.ID],
			deductionsByStaff[s.ID],
			extrasAddByStaff[s.ID],
			extrasDeductByStaff[s.ID],
		)
		balances[s.ID.String()] = result.Balance
	}

	c.JSON(http.StatusOK, balances)
}
