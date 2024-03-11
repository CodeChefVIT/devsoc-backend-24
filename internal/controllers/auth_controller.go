package controllers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/CodeChefVIT/devsoc-backend-24/internal/database"
	"github.com/CodeChefVIT/devsoc-backend-24/internal/models"
	services "github.com/CodeChefVIT/devsoc-backend-24/internal/services/user"
	"github.com/CodeChefVIT/devsoc-backend-24/internal/utils"
)

func Login(ctx echo.Context) error {
	var payload models.LoginRequest

	if err := ctx.Bind(&payload); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"message": err.Error(),
			"status":  "fail",
		})
	}

	if err := ctx.Validate(&payload); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"message": err.Error(),
			"status":  "fail",
		})
	}

	user, err := services.FindUserByEmail(payload.Email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"message": "user does not exist",
				"status":  "fail",
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"messsage": err.Error(),
			"status":   "error",
		})
	}

	if !user.IsVerified {
		return ctx.JSON(http.StatusForbidden, map[string]string{
			"message": "User is not verified",
			"status":  "fail",
		})
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(payload.Password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return ctx.JSON(http.StatusConflict, map[string]string{
				"message": "Invalid password",
				"status":  "fail",
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "error",
		})
	}

	if !user.IsProfileComplete {
		return ctx.JSON(http.StatusLocked, map[string]interface{}{
			"message": "profile not completed",
			"status":  "fail",
		})
	}

	tokenVersionStr, err := database.RedisClient.Get(
		fmt.Sprintf("token_version:%s", user.User.Email))
	if err != nil && err != redis.Nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"status":  "error",
			"message": err.Error(),
		})
	}

	tokenVersion, _ := strconv.Atoi(tokenVersionStr)

	accessToken, err := utils.CreateToken(utils.TokenPayload{
		Exp:          time.Minute * 5,
		Email:        user.User.Email,
		Role:         user.Role,
		TokenVersion: tokenVersion + 1,
	}, utils.ACCESS_TOKEN)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "error",
		})
	}

	refreshToken, err := utils.CreateToken(utils.TokenPayload{
		Exp:   time.Hour * 1,
		Email: user.User.Email,
	}, utils.REFRESH_TOKEN)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "error",
		})
	}

	if err := database.RedisClient.Set(fmt.Sprintf("token_version:%s", user.User.Email),
		fmt.Sprint(tokenVersion+1), time.Hour*1); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"status":  "error",
			"message": err.Error(),
		})
	}

	if err := database.RedisClient.Set(user.User.Email, refreshToken, time.Hour*1); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"status":  "error",
			"message": err.Error(),
		})
	}

	ctx.SetCookie(&http.Cookie{
		Name:     os.Getenv("ACCESS_COOKIE_NAME"),
		Value:    accessToken,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode, // CHANGE DURING PRODUCTION
		MaxAge:   86400,
		Secure:   true,
	})

	ctx.SetCookie(&http.Cookie{
		Name:     os.Getenv("REFRESH_COOKIE_NAME"),
		Value:    refreshToken,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode, // CHANGE DURING PRODUCTION
		MaxAge:   86400,
		Secure:   true,
	})

	if !user.IsProfileComplete {
		return ctx.JSON(http.StatusLocked, map[string]interface{}{
			"message": "login successful",
			"status":  "success",
			"data": map[string]interface{}{
				"profile_complete": user.IsProfileComplete,
			},
		})
	}

	return ctx.JSON(http.StatusOK, map[string]interface{}{
		"message": "login successful",
		"status":  "success",
	})
}

func Logout(ctx echo.Context) error {
	refreshToken := ctx.Get("user").(*jwt.Token)
	claims := refreshToken.Claims.(jwt.MapClaims)

	_, err := database.RedisClient.Get(claims["sub"].(string))
	if err != nil && err != redis.Nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "redis get",
		})
	}

	if err := database.RedisClient.Delete(claims["sub"].(string)); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "redis failure",
		})
	}

	if err := database.RedisClient.Delete("token_version:" + claims["sub"].(string)); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "redis failure",
		})
	}

	refreshCookie, err := ctx.Cookie(os.Getenv("REFRESH_COOKIE_NAME"))
	if err != nil {
		if errors.Is(err, echo.ErrCookieNotFound) {
			refreshCookie = &http.Cookie{
				Name:     os.Getenv("REFRESH_COOKIE_NAME"),
				HttpOnly: true,
				SameSite: http.SameSiteNoneMode, // CHANGE DURING PRODUCTION
				MaxAge:   -1,
				Secure:   true,
			}
		}
	}

	accessCookie, err := ctx.Cookie(os.Getenv("ACCESS_COOKIE_NAME"))
	if err != nil {
		if errors.Is(err, echo.ErrCookieNotFound) {
			accessCookie = &http.Cookie{
				Name:     os.Getenv("ACCESS_COOKIE_NAME"),
				HttpOnly: true,
				SameSite: http.SameSiteNoneMode, // CHANGE DURING PRODUCTION
				MaxAge:   -1,
				Secure:   true,
			}
		}
	}

	refreshCookie.MaxAge = -1
	accessCookie.MaxAge = -1

	ctx.SetCookie(accessCookie)
	ctx.SetCookie(refreshCookie)

	return ctx.JSON(http.StatusOK, map[string]string{
		"message": "logout successful",
		"status":  "success",
	})
}

func Refresh(ctx echo.Context) error {
	refreshToken := ctx.Get("user").(*jwt.Token)
	claims := refreshToken.Claims.(jwt.MapClaims)

	refreshCookie, _ := ctx.Cookie(os.Getenv("REFRESH_COOKIE_NAME"))

	accessCookie, err := ctx.Cookie(os.Getenv("ACCESS_COOKIE_NAME"))
	if err != nil {
		if !errors.Is(err, echo.ErrCookieNotFound) {
			return ctx.JSON(http.StatusBadRequest, map[string]string{
				"message": err.Error(),
				"status":  "get cookie",
			})
		}
		accessCookie = &http.Cookie{
			Name:     os.Getenv("ACCESS_COOKIE_NAME"),
			HttpOnly: true,
			SameSite: http.SameSiteNoneMode, // CHANGE DURING PRODUCTION
			MaxAge:   86400,
			Secure:   true,
		}
	}

	storedToken, err := database.RedisClient.Get(claims["sub"].(string))
	if err != nil {
		if err == redis.Nil {
			return ctx.JSON(http.StatusUnauthorized, map[string]string{
				"message": "please login again",
				"status":  "success",
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "redis get",
		})
	}

	if storedToken != refreshCookie.Value {
		return ctx.JSON(http.StatusConflict, map[string]string{
			"message": "invalid refresh token",
			"status":  "failure",
		})
	}

	user, err := services.FindUserByEmail(claims["sub"].(string))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"message": "user does not exist",
				"status":  "failure",
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"messsage": err.Error(),
			"status":   "db error",
		})
	}

	tokenVersionStr, err := database.RedisClient.Get("token_version:" + user.User.Email)
	if err != nil {
		if err != redis.Nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{
				"message": err.Error(),
				"status":  "redis get",
			})
		}
		tokenVersionStr = "0"
	}

	tokenVersion, _ := strconv.Atoi(tokenVersionStr)

	accessToken, err := utils.CreateToken(utils.TokenPayload{
		Exp:          time.Minute * 5,
		Email:        user.User.Email,
		Role:         user.Role,
		TokenVersion: tokenVersion + 1,
	}, utils.ACCESS_TOKEN)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "create token",
		})
	}

	if err := database.RedisClient.Set("token_version:"+user.User.Email, fmt.Sprint(tokenVersion+1), time.Hour*1); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"message": err.Error(),
			"status":  "redis set",
		})
	}

	accessCookie.Value = accessToken

	ctx.SetCookie(accessCookie)

	return ctx.JSON(http.StatusOK, map[string]string{
		"message": "token refreshed",
		"status":  "success",
	})
}
