package securetransport

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func protectPrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	inheritance := ""
	if info.IsDir() {
		inheritance = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;%s;FA;;;%s)(A;%s;FA;;;SY)", inheritance, user.User.Sid.String(), inheritance))
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
