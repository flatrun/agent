package api

import (
	"net/http"
	"strconv"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/scheduler"
	"github.com/gin-gonic/gin"
)

func (s *Server) listScheduledTasks(c *gin.Context) {
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Scheduler not enabled"})
		return
	}

	deploymentName := c.Query("deployment")

	var tasks []scheduler.ScheduledTask
	var err error

	if deploymentName != "" {
		if !s.requireDeploymentAccess(c, deploymentName, auth.AccessLevelRead) {
			return
		}
		tasks, err = s.schedulerManager.GetTasksByDeployment(deploymentName)
	} else {
		tasks, err = s.schedulerManager.GetAllTasks()
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		filtered := tasks[:0]
		for _, task := range tasks {
			if actor.CanAccessDeployment(task.DeploymentName, auth.AccessLevelRead) {
				filtered = append(filtered, task)
			}
		}
		tasks = filtered
	}

	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}

func (s *Server) getScheduledTask(c *gin.Context) {
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Scheduler not enabled"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid task ID"})
		return
	}

	task, err := s.schedulerManager.GetTask(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if !s.requireDeploymentAccess(c, task.DeploymentName, auth.AccessLevelRead) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"task": task})
}

func (s *Server) createScheduledTask(c *gin.Context) {
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Scheduler not enabled"})
		return
	}

	var req scheduler.CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !s.requireDeploymentAccess(c, req.DeploymentName, auth.AccessLevelWrite) {
		return
	}

	if err := s.schedulerManager.ValidateCronExpr(req.CronExpr); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid cron expression: " + err.Error()})
		return
	}

	_, err := s.manager.GetDeployment(req.DeploymentName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deployment not found: " + req.DeploymentName})
		return
	}

	if req.Config.CommandConfig != nil {
		resolved, err := s.resolveService(req.DeploymentName, req.Config.CommandConfig.Service)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		req.Config.CommandConfig.Service = resolved
	}

	task, err := s.schedulerManager.CreateTask(&req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"task": task})
}

func (s *Server) updateScheduledTask(c *gin.Context) {
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Scheduler not enabled"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid task ID"})
		return
	}

	existingTask, err := s.schedulerManager.GetTask(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if !s.requireDeploymentAccess(c, existingTask.DeploymentName, auth.AccessLevelWrite) {
		return
	}

	var req scheduler.UpdateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.CronExpr != nil {
		if err := s.schedulerManager.ValidateCronExpr(*req.CronExpr); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid cron expression: " + err.Error()})
			return
		}
	}

	if req.Config != nil && req.Config.CommandConfig != nil {
		resolved, err := s.resolveService(existingTask.DeploymentName, req.Config.CommandConfig.Service)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		req.Config.CommandConfig.Service = resolved
	}

	task, err := s.schedulerManager.UpdateTask(id, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"task": task})
}

func (s *Server) deleteScheduledTask(c *gin.Context) {
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Scheduler not enabled"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid task ID"})
		return
	}
	task, err := s.schedulerManager.GetTask(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if !s.requireDeploymentAccess(c, task.DeploymentName, auth.AccessLevelWrite) {
		return
	}

	if err := s.schedulerManager.DeleteTask(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Task deleted successfully"})
}

func (s *Server) runTaskNow(c *gin.Context) {
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Scheduler not enabled"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid task ID"})
		return
	}
	task, err := s.schedulerManager.GetTask(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if !s.requireDeploymentAccess(c, task.DeploymentName, auth.AccessLevelWrite) {
		return
	}

	if err := s.schedulerManager.RunTaskNow(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"message": "Task execution started"})
}

func (s *Server) getTaskExecutions(c *gin.Context) {
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Scheduler not enabled"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid task ID"})
		return
	}
	task, err := s.schedulerManager.GetTask(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if !s.requireDeploymentAccess(c, task.DeploymentName, auth.AccessLevelRead) {
		return
	}

	limit := 50
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil {
			limit = parsed
		}
	}

	executions, err := s.schedulerManager.GetTaskExecutions(id, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"executions": executions})
}

func (s *Server) getRecentExecutions(c *gin.Context) {
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Scheduler not enabled"})
		return
	}

	limit := 50
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil {
			limit = parsed
		}
	}

	executions, err := s.schedulerManager.GetRecentExecutions(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		filtered := executions[:0]
		taskDeployments := make(map[int64]string)
		for _, execution := range executions {
			deploymentName, ok := taskDeployments[execution.TaskID]
			if !ok {
				task, err := s.schedulerManager.GetTask(execution.TaskID)
				if err != nil {
					continue
				}
				deploymentName = task.DeploymentName
				taskDeployments[execution.TaskID] = deploymentName
			}
			if actor.CanAccessDeployment(deploymentName, auth.AccessLevelRead) {
				filtered = append(filtered, execution)
			}
		}
		executions = filtered
	}

	c.JSON(http.StatusOK, gin.H{"executions": executions})
}
