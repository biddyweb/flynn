package client

import (
	"net/url"

	flag "github.com/flynn/flynn/Godeps/_workspace/src/github.com/docker/docker/pkg/mflag"
	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/docker/docker/pkg/parsers"
	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/docker/docker/registry"
	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/docker/docker/utils"
)

func (cli *DockerCli) CmdTag(args ...string) error {
	cmd := cli.Subcmd("tag", "IMAGE[:TAG] [REGISTRYHOST/][USERNAME/]NAME[:TAG]", "Tag an image into a repository", true)
	force := cmd.Bool([]string{"f", "#force", "-force"}, false, "Force")
	cmd.Require(flag.Exact, 2)

	utils.ParseFlags(cmd, args, true)

	var (
		repository, tag = parsers.ParseRepositoryTag(cmd.Arg(1))
		v               = url.Values{}
	)

	//Check if the given image name can be resolved
	if err := registry.ValidateRepositoryName(repository); err != nil {
		return err
	}
	v.Set("repo", repository)
	v.Set("tag", tag)

	if *force {
		v.Set("force", "1")
	}

	if _, _, err := readBody(cli.call("POST", "/images/"+cmd.Arg(0)+"/tag?"+v.Encode(), nil, false)); err != nil {
		return err
	}
	return nil
}
