package secret

import "errors"

func CanCreate(role Role) bool {
	return role == RoleOwner || role == RoleDeveloper
}

func CanReveal(role Role) bool {
	return role == RoleOwner
}

func CanRotate(role Role) bool {
	return role == RoleOwner
}

func RequireRole(auth AuthContext, allowed ...Role) error {
	for _, role := range allowed {
		if auth.Role == role {
			return nil
		}
	}
	return errors.New("permission denied")
}
