#include <stdio.h>
#include <unistd.h>
#include <sys/stat.h>
#include <pwd.h>
#include <string.h>
#include <errno.h>

int main(void) {
    struct stat st;
    char owner[64];
    struct passwd *pw = getpwuid(getuid());
    
    snprintf(owner, sizeof(owner), "%s", pw ? pw->pw_name : "unknown");
    
    if (stat("stub", &st) == 0) {
        printf("mode=%04o owner=%s uid=%d gid=%d\n", 
               st.st_mode & 07777, owner, getuid(), getgid());
    } else {
        printf("stat failed: %s\n", strerror(errno));
    }
    
    printf("effective uid=%d effective gid=%d\n", geteuid(), getegid());
    printf("real uid=%d real gid=%d\n", getuid(), getgid());
    
    return 0;
}
