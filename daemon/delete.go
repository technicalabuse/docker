package daemon

import (
	"fmt"
	"os"
	"path"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/engine"
)

func (daemon *Daemon) ContainerRm(job *engine.Job) error {
	if len(job.Args) != 1 {
		return fmt.Errorf("Not enough arguments. Usage: %s CONTAINER\n", job.Name)
	}
	name := job.Args[0]
	removeVolume := job.GetenvBool("removeVolume")
	removeLink := job.GetenvBool("removeLink")
	forceRemove := job.GetenvBool("forceRemove")

	container, err := daemon.Get(name)
	if err != nil {
		return err
	}

	if removeLink {
		name, err := GetFullContainerName(name)
		if err != nil {
			return err
			// TODO: why was just job.Error(err) without return if the function cannot continue w/o container name?
			//job.Error(err)
		}
		parent, n := path.Split(name)
		if parent == "/" {
			return fmt.Errorf("Conflict, cannot remove the default name of the container")
		}
		pe := daemon.ContainerGraph().Get(parent)
		if pe == nil {
			return fmt.Errorf("Cannot get parent %s for name %s", parent, name)
		}
		parentContainer, _ := daemon.Get(pe.ID())

		if parentContainer != nil {
			parentContainer.DisableLink(n)
		}

		if err := daemon.ContainerGraph().Delete(name); err != nil {
			return err
		}
		return nil
	}

	if container != nil {
		// stop collection of stats for the container regardless
		// if stats are currently getting collected.
		daemon.statsCollector.stopCollection(container)
		if container.IsRunning() {
			if forceRemove {
				if err := container.Kill(); err != nil {
					return fmt.Errorf("Could not kill running container, cannot remove - %v", err)
				}
			} else {
				return fmt.Errorf("Conflict, You cannot remove a running container. Stop the container before attempting removal or use -f")
			}
		}
		if err := daemon.Rm(container); err != nil {
			return fmt.Errorf("Cannot destroy container %s: %s", name, err)
		}
		container.LogEvent("destroy")
		if removeVolume {
			daemon.DeleteVolumes(container.VolumePaths())
		}
	}
	return nil
}

func (daemon *Daemon) DeleteVolumes(volumeIDs map[string]struct{}) {
	for id := range volumeIDs {
		if err := daemon.volumes.Delete(id); err != nil {
			logrus.Infof("%s", err)
			continue
		}
	}
}

// Destroy unregisters a container from the daemon and cleanly removes its contents from the filesystem.
func (daemon *Daemon) Rm(container *Container) error {
	if container == nil {
		return fmt.Errorf("The given container is <nil>")
	}

	element := daemon.containers.Get(container.ID)
	if element == nil {
		return fmt.Errorf("Container %v not found - maybe it was already destroyed?", container.ID)
	}

	if err := container.Stop(3); err != nil {
		return err
	}

	// Deregister the container before removing its directory, to avoid race conditions
	daemon.idIndex.Delete(container.ID)
	daemon.containers.Delete(container.ID)
	container.derefVolumes()
	if _, err := daemon.containerGraph.Purge(container.ID); err != nil {
		logrus.Debugf("Unable to remove container from link graph: %s", err)
	}

	if err := daemon.driver.Remove(container.ID); err != nil {
		return fmt.Errorf("Driver %s failed to remove root filesystem %s: %s", daemon.driver, container.ID, err)
	}

	initID := fmt.Sprintf("%s-init", container.ID)
	if err := daemon.driver.Remove(initID); err != nil {
		return fmt.Errorf("Driver %s failed to remove init filesystem %s: %s", daemon.driver, initID, err)
	}

	if err := os.RemoveAll(container.root); err != nil {
		return fmt.Errorf("Unable to remove filesystem for %v: %v", container.ID, err)
	}

	if err := daemon.execDriver.Clean(container.ID); err != nil {
		return fmt.Errorf("Unable to remove execdriver data for %s: %s", container.ID, err)
	}

	selinuxFreeLxcContexts(container.ProcessLabel)

	return nil
}
