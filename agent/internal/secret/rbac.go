package secret

func CanCreate(role Role) bool {
	return role == RoleOwner || role == RoleDeveloper
}

func CanReveal(role Role) bool {
	return role == RoleOwner
}

func CanRotate(role Role) bool {
	return role == RoleOwner
}
