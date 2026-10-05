#define _POSIX_C_SOURCE 200809L

#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static void terminate(int signal_number)
{
    (void)signal_number;
    _exit(42);
}

static int orphan(void)
{
    int pipefd[2];
    if (pipe(pipefd) == -1) {
        perror("pipe");
        return 1;
    }
    pid_t child = fork();
    if (child == -1) {
        perror("fork");
        return 1;
    }
    if (child == 0) {
        close(pipefd[0]);
        pid_t grandchild = fork();
        if (grandchild == -1) {
            perror("fork grandchild");
            _exit(1);
        }
        if (grandchild == 0) {
            close(pipefd[1]);
            _exit(0);
        }
        if (write(pipefd[1], &grandchild, sizeof(grandchild)) != sizeof(grandchild)) {
            perror("write");
            _exit(1);
        }
        _exit(0);
    }
    close(pipefd[1]);
    pid_t grandchild;
    ssize_t received = read(pipefd[0], &grandchild, sizeof(grandchild));
    close(pipefd[0]);
    int status;
    if (waitpid(child, &status, 0) != child) {
        perror("waitpid");
        return 1;
    }
    if (received != sizeof(grandchild) || !WIFEXITED(status) || WEXITSTATUS(status) != 0) {
        fputs("failed to create orphan\n", stderr);
        return 1;
    }
    for (int attempt = 0; attempt < 100; attempt++) {
        if (kill(grandchild, 0) == -1) {
            if (errno == ESRCH) {
                puts("orphan reaped");
                return 0;
            }
            perror("kill");
            return 1;
        }
        struct timespec delay = { .tv_sec = 0, .tv_nsec = 50000000 };
        if (nanosleep(&delay, NULL) == -1) {
            perror("nanosleep");
            return 1;
        }
    }
    fputs("orphan was not reaped within five seconds\n", stderr);
    return 1;
}

int main(int argc, char **argv)
{
    if (argc == 2 && strcmp(argv[1], "exit") == 0) {
        return 42;
    }
    if (argc == 2 && strcmp(argv[1], "orphan") == 0) {
        return orphan();
    }
    if (argc == 2 && strcmp(argv[1], "signal") == 0) {
        struct sigaction action = { .sa_handler = terminate };
        if (sigemptyset(&action.sa_mask) == -1 || sigaction(SIGTERM, &action, NULL) == -1) {
            perror("sigaction");
            return 1;
        }
        puts("ready");
        fflush(stdout);
        for (;;) {
            pause();
        }
    }
    fputs("expected exit, orphan, or signal\n", stderr);
    return 1;
}
